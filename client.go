package socketio

import (
	"net/http"
	"net/url"
	"reflect"
	"github.com/zhouhui8915/engine.io-go"
)

var defaultTransport = "websocket"

type Options struct {
	Transport string            //protocol name string,websocket polling...
	Query     map[string]string //url的附加的参数

}

type Client struct {
	opts *Options

	conn *engineio.ClientConn

	events map[string]*caller
	acks   map[int]*caller
	id        int
	namespace string
}

func NewClient(uri string, opts *Options) (client *Client, err error) {

	request := &http.Request{}
	request.URL, err = url.Parse(uri)
	if err != nil {
		return
	}
	q := request.URL.Query()
	for k, v := range opts.Query {
		q.Set(k, v)
	}
	request.URL.RawQuery = q.Encode()

	socket, err := engineio.NewClientConn(opts.Transport, request)
	if err != nil {
		return
	}

	client = &Client{
		opts:   opts,
		conn: socket,

		events: make(map[string]*caller),
		acks:   make(map[int]*caller),
	}

	go client.readLoop()

	return
}

func (client *Client) On(message string, f interface{}) (err error) {
	c, err := newCaller(f)
	if err != nil {
		return
	}
	client.events[message] = c
	return
}

func (client *Client) Emit(message string, args ...interface{}) (err error) {
	var c *caller
	if l := len(args); l > 0 {
		fv := reflect.ValueOf(args[l-1])
		if fv.Kind() == reflect.Func {
			var err error
			c, err = newCaller(args[l-1])
			if err != nil {
				return err
			}
			args = args[:l-1]
		}
	}
	args = append([]interface{}{message}, args...)
	if c != nil {
		id, err := client.sendId(args)
		if err != nil {
			return err
		}
		client.acks[id] = c
		return nil
	}
	return client.send(args)
}

func (client *Client) sendConnect() error {
	packet := packet{
		Type: _CONNECT,
		Id:   -1,
		NSP:  client.namespace,
	}
	encoder := newEncoder(client.conn)
	return encoder.Encode(packet)
}

func (client *Client) sendId(args []interface{}) (int, error) {
	packet := packet{
		Type: _EVENT,
		Id:   client.id,
		NSP:  client.namespace,
		Data: args,
	}
	client.id++
	if client.id < 0 {
		client.id = 0
	}
	encoder := newEncoder(client.conn)
	err := encoder.Encode(packet)
	if err != nil {
		return -1, nil
	}
	return packet.Id, nil
}

func (client *Client) send(args []interface{}) error {
	packet := packet{
		Type: _EVENT,
		Id:   -1,
		NSP:  client.namespace,
		Data: args,
	}
	encoder := newEncoder(client.conn)
	return encoder.Encode(packet)
}

func (client *Client) onPacket(decoder *decoder, packet *packet) ([]interface{}, error) {
	var message string
	switch packet.Type {
	case _CONNECT:
		message = "connection"
	case _DISCONNECT:
		message = "disconnection"
	case _ERROR:
		message = "error"
	case _ACK:
	case _BINARY_ACK:
		return nil, client.onAck(packet.Id, decoder, packet)
	default:
		message = decoder.Message()
	}
	c, ok := client.events[message]
	if !ok {
		// If the message is not recognized by the server, the decoder.currentCloser
		// needs to be closed otherwise the server will be stuck until the e
		decoder.Close()
		return nil, nil
	}
	args := c.GetArgs()
	olen := len(args)
	if olen > 0 {
		packet.Data = &args
		if err := decoder.DecodeData(packet); err != nil {
			return nil, err
		}
	}
	for i := len(args); i < olen; i++ {
		args = append(args, nil)
	}

	retV := c.Call(nil,args)
	if len(retV) == 0 {
		return nil, nil
	}

	var err error
	if last, ok := retV[len(retV)-1].Interface().(error); ok {
		err = last
		retV = retV[0 : len(retV)-1]
	}
	ret := make([]interface{}, len(retV))
	for i, v := range retV {
		ret[i] = v.Interface()
	}
	return ret, err
}

func (client *Client) onAck(id int, decoder *decoder, packet *packet) error {
	c, ok := client.acks[id]
	if !ok {
		return nil
	}
	delete(client.acks, id)

	args := c.GetArgs()
	packet.Data = &args
	if err := decoder.DecodeData(packet); err != nil {
		return err
	}
	c.Call(nil,args)
	return nil
}

func (client *Client) readLoop() error {
	defer func() {
		p := packet{
			Type: _DISCONNECT,
			Id:   -1,
		}
		client.onPacket(nil, &p)
	}()

	for {
		decoder := newDecoder(client.conn)
		var p packet
		if err := decoder.Decode(&p); err != nil {
			return err
		}
		ret, err := client.onPacket(decoder, &p)
		if err != nil {
			return err
		}
		switch p.Type {
		case _CONNECT:
			client.namespace = p.NSP
			// !!!下面这个不能有，否则会有死循环
			//client.sendConnect()
		case _BINARY_EVENT:
			fallthrough
		case _EVENT:
			if p.Id >= 0 {
				p := packet{
					Type: _ACK,
					Id:   p.Id,
					NSP:  client.namespace,
					Data: ret,
				}
				encoder := newEncoder(client.conn)
				if err := encoder.Encode(p); err != nil {
					return err
				}
			}
		case _DISCONNECT:
			return nil
		}
	}
}
