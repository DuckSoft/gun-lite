package realgun

import (
	"bytes"
	"crypto/tls"
	"ekyu.moe/leb128"
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/net/context"
	"golang.org/x/net/http2"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type GunConn struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
	local net.Addr
	remote net.Addr
	done chan struct{}
}

type Client struct {
	ctx context.Context
	client *http.Client
	url *url.URL
	headers http.Header
}

type Config struct {
	RemoteAddr string
	ServerName string
	ServiceName string
	Cleartext bool
}

func NewGunClientWithContext(ctx context.Context, config *Config) *Client {
	var dialFunc func(network, addr string, cfg *tls.Config) (net.Conn, error) = nil
	if config.Cleartext {
		dialFunc = func(network, addr string, cfg *tls.Config) (net.Conn, error) {
			return net.Dial(network, addr)
		}
	}

	var tlsClientConfig *tls.Config = nil
	if config.ServerName != "" {
		tlsClientConfig = new(tls.Config)
		tlsClientConfig.ServerName = config.ServerName
	}

	client := &http.Client{
		Transport:     &http2.Transport{
			DialTLS:                    dialFunc,
			TLSClientConfig:            tlsClientConfig,
			AllowHTTP: false,
			DisableCompression:         true,
			ReadIdleTimeout:            0,
			PingTimeout:                0,
		},
	}

	var serviceName string = "GunService"
	if config.ServiceName != "" {
		serviceName = config.ServiceName
	}

	return &Client{
		ctx:    ctx,
		client: client,
		url:    &url.URL{
			Scheme:      "https",
			Host:        config.RemoteAddr,
			Path:        fmt.Sprintf("/%s/Tun", serviceName),
		},
		headers: http.Header{
			"content-type": []string{"application/grpc+proto"},
			"user-agent":   []string{"grpc-java/1.2.3"},
		},
	}
}

type ChainedClosable []io.Closer

// Close implements io.Closer.Close().
func (cc ChainedClosable) Close() error {
	for _, c := range cc {
		_ = c.Close()
	}
	return nil
}


func (cli *Client) DialConn() (net.Conn, error) {
	reader, writer := io.Pipe()
	request := &http.Request{
		Method:           http.MethodPost,
		Body: reader,
		URL:              cli.url,
		Proto: "HTTP/2",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header: cli.headers,
	}
	response, err := cli.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, net.ErrClosed
	}
	return newGunConn(response.Body, writer, ChainedClosable{reader, writer, response.Body}, nil, nil), nil
}

var (
	ErrInvalidLength = errors.New("invalid length")
)

func newGunConn(reader io.Reader, writer io.Writer, closer io.Closer, local net.Addr, remote net.Addr) *GunConn {
	if local == nil {
		local = &net.TCPAddr{
			IP:   []byte{0, 0, 0, 0},
			Port: 0,
		}
	}
	if remote == nil {
		remote = &net.TCPAddr{
			IP:   []byte{0, 0, 0, 0},
			Port: 0,
		}
	}
	return &GunConn{
		reader: reader,
		writer: writer,
		closer: closer,
		local:  local,
		remote: remote,
		done:   make(chan struct{}),
	}
}

func (g *GunConn) isClosed() bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}

func (g GunConn) Read(b []byte) (n int, err error) {
	grpcHeader := make([]byte, 7)
	n, err = io.ReadFull(g.reader, grpcHeader)
	if err != nil {
		return 0, err
	}
	grpcPayloadLen := binary.BigEndian.Uint32(grpcHeader[1:5])

	protobufPayloadLen, protobufLengthLen := leb128.DecodeUleb128(grpcHeader[6:])
	if protobufLengthLen == 0 {
		return 0, ErrInvalidLength
	}
	if grpcPayloadLen != uint32(protobufPayloadLen)+uint32(protobufLengthLen)+1 {
		return 0, ErrInvalidLength
	}

	n, err = io.MultiReader(bytes.NewReader(grpcHeader[6+protobufLengthLen:n]), io.LimitReader(g.reader, int64(int(grpcPayloadLen)+5-n))).Read(b)
	return n, err

}

func (g GunConn) Write(b []byte) (n int, err error) {
	if g.isClosed() {
		return 0, io.ErrClosedPipe
	}
	protobufHeader := leb128.AppendUleb128([]byte{0x0A}, uint64(len(b)))
	grpcHeader := make([]byte, 5)
	grpcPayloadLen := uint32(len(protobufHeader) + len(b))
	binary.BigEndian.PutUint32(grpcHeader[1:5], grpcPayloadLen)
	_, err = io.Copy(g.writer, io.MultiReader(bytes.NewReader(grpcHeader), bytes.NewReader(protobufHeader), bytes.NewReader(b)))
	return len(b), err
}

func (g GunConn) Close() error {
	defer close(g.done)
	err := g.closer.Close()
	return err
}

func (g GunConn) LocalAddr() net.Addr {
	return g.local
}

func (g GunConn) RemoteAddr() net.Addr {
	return g.remote
}

func (g GunConn) SetDeadline(t time.Time) error {
	return nil
}

func (g GunConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (g GunConn) SetWriteDeadline(t time.Time) error {
	return nil
}

