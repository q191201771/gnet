// Copyright 2019 Andy Pan. All rights reserved.
// Copyright 2018 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// +build windows

package gnet

import (
	"net"

	"github.com/panjf2000/gnet/pool/bytebuffer"
	prb "github.com/panjf2000/gnet/pool/ringbuffer"
	"github.com/panjf2000/gnet/ringbuffer"
)

type stderr struct {
	c   *stdConn
	err error
}

type wakeReq struct {
	c *stdConn
}

type tcpIn struct {
	c  *stdConn
	in *bytebuffer.ByteBuffer
}

type udpIn struct {
	c *stdConn
}

type stdConn struct {
	ctx           interface{}            // user-defined context
	conn          net.Conn               // original connection
	loop          *eventloop             // owner event-loop
	done          int32                  // 0: attached, 1: closed
	buffer        *bytebuffer.ByteBuffer // reuse memory of inbound data as a temporary buffer
	codec         ICodec                 // codec for TCP
	localAddr     net.Addr               // local server addr
	remoteAddr    net.Addr               // remote peer addr
	byteBuffer    *bytebuffer.ByteBuffer // bytes buffer for buffering current packet and data in ring-buffer
	inboundBuffer *ringbuffer.RingBuffer // buffer for data from client
}

func newTCPConn(conn net.Conn, el *eventloop) *stdConn {
	return &stdConn{
		conn:          conn,
		loop:          el,
		codec:         el.codec,
		inboundBuffer: prb.Get(),
	}
}

func (c *stdConn) releaseTCP() {
	c.ctx = nil
	c.localAddr = nil
	c.remoteAddr = nil
	prb.Put(c.inboundBuffer)
	c.inboundBuffer = nil
	bytebuffer.Put(c.buffer)
	c.buffer = nil
}

func newUDPConn(el *eventloop, localAddr, remoteAddr net.Addr, buf *bytebuffer.ByteBuffer) *stdConn {
	return &stdConn{
		loop:       el,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		buffer:     buf,
	}
}

func (c *stdConn) releaseUDP() {
	c.ctx = nil
	c.localAddr = nil
	bytebuffer.Put(c.buffer)
	c.buffer = nil
}

func (c *stdConn) read() ([]byte, error) {
	return c.codec.Decode(c)
}

// ================================= Public APIs of gnet.Conn =================================

func (c *stdConn) Read() []byte {
	if c.inboundBuffer.IsEmpty() {
		if c.buffer.Len() == 0 {
			return nil
		}
		return c.buffer.Bytes()
	}
	c.byteBuffer = c.inboundBuffer.WithByteBuffer(c.buffer.Bytes())
	return c.byteBuffer.Bytes()
}

func (c *stdConn) ResetBuffer() {
	c.buffer.Reset()
	c.inboundBuffer.Reset()
	bytebuffer.Put(c.byteBuffer)
	c.byteBuffer = nil
}

func (c *stdConn) ReadN(n int) (size int, buf []byte) {
	inBufferLen := c.inboundBuffer.Length()
	tempBufferLen := c.buffer.Len()
	if totalLen := inBufferLen + tempBufferLen; totalLen < n || n <= 0 {
		n = totalLen
	}
	size = n
	if c.inboundBuffer.IsEmpty() {
		buf = c.buffer.B[:n]
		return
	}
	head, tail := c.inboundBuffer.LazyRead(n)
	c.byteBuffer = bytebuffer.Get()
	_, _ = c.byteBuffer.Write(head)
	_, _ = c.byteBuffer.Write(tail)
	if inBufferLen >= n {
		buf = c.byteBuffer.Bytes()
		return
	}

	restSize := n - inBufferLen
	_, _ = c.byteBuffer.Write(c.buffer.B[:restSize])
	buf = c.byteBuffer.Bytes()
	return
}

func (c *stdConn) ShiftN(n int) (size int) {
	inBufferLen := c.inboundBuffer.Length()
	tempBufferLen := c.buffer.Len()
	if inBufferLen+tempBufferLen < n || n <= 0 {
		c.ResetBuffer()
		size = inBufferLen + tempBufferLen
		return
	}
	size = n
	if c.inboundBuffer.IsEmpty() {
		c.buffer.B = c.buffer.B[n:]
		return
	}

	bytebuffer.Put(c.byteBuffer)
	c.byteBuffer = nil

	if inBufferLen >= n {
		c.inboundBuffer.Shift(n)
		return
	}
	c.inboundBuffer.Reset()

	restSize := n - inBufferLen
	c.buffer.B = c.buffer.B[restSize:]
	return
}

func (c *stdConn) BufferLength() int {
	return c.inboundBuffer.Length() + c.buffer.Len()
}

func (c *stdConn) AsyncWrite(buf []byte) (err error) {
	var encodedBuf []byte
	if encodedBuf, err = c.codec.Encode(c, buf); err == nil {
		c.loop.ch <- func() error {
			_, _ = c.conn.Write(encodedBuf)
			return nil
		}
	}
	return
}

func (c *stdConn) SendTo(buf []byte) (err error) {
	_, err = c.loop.svr.ln.pconn.WriteTo(buf, c.remoteAddr)
	return
}

func (c *stdConn) Wake() error {
	c.loop.ch <- wakeReq{c}
	return nil
}

func (c *stdConn) Close() error {
	c.loop.ch <- func() error {
		return c.loop.loopCloseConn(c)
	}
	return nil
}

func (c *stdConn) Context() interface{}       { return c.ctx }
func (c *stdConn) SetContext(ctx interface{}) { c.ctx = ctx }
func (c *stdConn) LocalAddr() net.Addr        { return c.localAddr }
func (c *stdConn) RemoteAddr() net.Addr       { return c.remoteAddr }
