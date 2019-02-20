package service

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/remerge/cue"
)

const (
	debugForwarderMaxConn      uint32 = 100
	debugForwarderWriteTimeout        = time.Millisecond * 500
)

// DebugForwarder accepts connections on given port and multiplicities
// feeding data to incoming connections.
type DebugForwarder struct {
	Port int
	log  cue.Logger

	listener  net.Listener
	conns     sync.Map
	connCount uint32

	closing uint32

	// TODO (spin): Remove these metrics after proof correct behaviour

	connAcceptCounter metrics.Counter
	connCloseCounter  metrics.Counter
}

func NewDebugForwarder(logger cue.Logger, metricsRegistry metrics.Registry, port int) (f *DebugForwarder, err error) {
	f = &DebugForwarder{
		log:               logger,
		Port:              port,
		connAcceptCounter: metrics.GetOrRegisterCounter("debug conn_accept", metricsRegistry),
		connCloseCounter:  metrics.GetOrRegisterCounter("debug conn_close", metricsRegistry),
	}
	f.listener, err = net.Listen("tcp", fmt.Sprintf(":%d", f.Port))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize debug listening socket: %v", err)
	}

	f.log.WithFields(cue.Fields{"port": f.Port}).Info("start debug listener")

	go func(ln net.Listener) {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				_ = f.log.Error(acceptErr, "failed to accept debug listener connection, terminate loop")
				break
			}
			f.log.WithFields(cue.Fields{"remote_addr": conn.RemoteAddr().String()}).Info("debug connection opened")

			if atomic.LoadUint32(&f.closing) == 1 {
				f.log.WithFields(cue.Fields{"remote_addr": conn.RemoteAddr().String()}).Warnf("dropping conn: closed")
				_, _ = conn.Write([]byte("closed\n"))
				_ = conn.Close()
				break
			}

			if debugForwarderMaxConn > atomic.LoadUint32(&f.connCount) {
				f.log.WithFields(cue.Fields{"remote_addr": conn.RemoteAddr().String()}).Warnf("dropping conn: max reached")
				_, _ = conn.Write([]byte("max conn reached\n"))
				_ = conn.Close()
				continue
			}
			atomic.AddUint32(&f.connCount, 1)
			f.conns.Store(conn.RemoteAddr().String(), conn)
			f.connAcceptCounter.Inc(1)
		}
	}(f.listener)
	return f, nil
}

func (f *DebugForwarder) Close() error {
	if f == nil || !atomic.CompareAndSwapUint32(&f.closing, 0, 1) {
		return nil
	}
	_ = f.listener.Close()
	f.conns.Range(func(k, v interface{}) bool {
		c := v.(net.Conn)
		_ = c.Close()
		return true
	})
	return nil
}

func (f *DebugForwarder) HasOpenConnections() bool {
	if f == nil || atomic.LoadUint32(&f.closing) == 1 {
		return false
	}
	return atomic.LoadUint32(&f.connCount) > 0
}

func (f *DebugForwarder) Write(data []byte) (n int, err error) {
	if f == nil || atomic.LoadUint32(&f.closing) == 1 {
		return 0, nil
	}
	if atomic.LoadUint32(&f.connCount) == 0 {
		return 0, nil
	}

	f.conns.Range(func(k, v interface{}) bool {
		c := v.(net.Conn)
		go func(k interface{}, c net.Conn, d []byte) {
			var badConn bool
			if setErr := c.SetWriteDeadline(time.Now().Add(debugForwarderWriteTimeout)); setErr != nil {
				badConn = true
			}
			if !badConn {
				if _, writeErr := c.Write(data); writeErr != nil {
					_ = c.Close()
					if _, ok := f.conns.Load(k); ok {
						f.log.WithFields(cue.Fields{"source": k}).Info("debug connection closed")
					}
					badConn = true
				}
			}
			if badConn {
				f.conns.Delete(k)
				atomic.AddUint32(&f.connCount, ^uint32(0))
				f.connCloseCounter.Inc(1)
			}

		}(k, c, data)
		return true
	})
	return len(data), nil
}
