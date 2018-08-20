package service

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/remerge/cue"
)

func (e *Executor) WithDebugForwarder(port int) *Executor {
	e.debugForwader = &debugForwader{
		Port: port,
		log:  e.Log,
	}
	flags := e.Command.Flags()

	flags.IntVar(
		&e.debugForwader.Port,
		"debug-fwd-port", e.debugForwader.Port,
		"Debug forwarding port",
	)
	return e
}

func (e *Executor) ForwardToDebugConns(data []byte) {
	e.debugForwader.forward(data)
}

func (e *Executor) HasOpenDebugForwardingConns() bool {
	return e.debugForwader.hasOpenConnections()
}

type debugForwader struct {
	sync.Mutex
	Port      int
	conns     sync.Map
	connCount uint32
	connLn    net.Listener
	log       *Logger
}

// initDebugForwardingListener
func (f *debugForwader) init() error {
	if f.Port == 0 {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", f.Port))
	if err != nil {
		return fmt.Errorf("failed to initialize debug listening socket: %v", err)
	}
	f.log.WithFields(cue.Fields{"port": f.Port}).Info("start debug listener")
	f.connLn = ln
	go func(ln net.Listener) {
		for {
			c, err := ln.Accept()
			if err != nil {
				f.log.Error(err, "failed to accept debug listener connection, terminate loop")
				break
			}
			f.log.WithFields(cue.Fields{"remote_addr": c.RemoteAddr().String()}).Info("debug connection opened")
			atomic.AddUint32(&f.connCount, 1)
			f.conns.Store(c.RemoteAddr().String(), c)
		}
	}(ln)
	return nil
}

func (f *debugForwader) shutdown() {
	if f == nil {
		return
	}
	if f.connLn != nil {
		f.connLn.Close()
	}

	f.conns.Range(func(k, v interface{}) bool {
		c := v.(net.Conn)
		c.Close()
		return true
	})
}

func (f *debugForwader) hasOpenConnections() bool {
	if f == nil {
		return false
	}
	return atomic.LoadUint32(&f.connCount) > 0
}

func (f *debugForwader) forward(data []byte) {
	if f == nil {
		return
	}
	if atomic.LoadUint32(&f.connCount) == 0 {
		return
	}
	f.conns.Range(func(k, v interface{}) bool {
		c := v.(net.Conn)
		go func(k interface{}, c net.Conn, d []byte) {
			_, err := c.Write(d)
			if err != nil {
				f.Lock()
				if _, ok := f.conns.Load(k); ok {
					f.log.WithFields(cue.Fields{"source": k}).Info("debug connection closed")
					f.conns.Delete(k)
					atomic.AddUint32(&f.connCount, ^uint32(0))
				}
				f.Unlock()
			}
		}(k, c, data)
		return true
	})
}
