package service

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/remerge/cue"
	"github.com/remerge/go-service/registry"
)

const debugForwarderMaxConn = 64

type DebugForwaderConfig struct {
	Port int
}

type DebugForwaderParams struct {
	registry.Params
	DebugForwaderConfig `registry:"lazy"`
	Log                 cue.Logger
	Cmd                 *cobra.Command
}

func newDebugForwader(params *DebugForwaderParams) (*debugForwader, error) {
	f := &debugForwader{
		Port:   params.Port,
		log:    params.Log,
		quit:   make(chan bool),
		exited: make(chan bool),
	}
	f.configureFlags(params.Cmd)
	return f, nil
}

func (f *debugForwader) configureFlags(cmd *cobra.Command) {
	cmd.Flags().IntVar(
		&f.Port,
		"debug-fwd-port", f.Port,
		"Debug forwarding port",
	)
}

type debugForwader struct {
	Port      int
	connsLock sync.RWMutex
	conns     map[string]*debugConn
	connCount uint32
	connLn    net.Listener
	log       cue.Logger
	quit      chan bool
	exited    chan bool
	closing   uint32
}

type debugConn struct {
	net.Conn
	o         sync.Once
	closeWait sync.WaitGroup
	msgs      chan []byte
	forwarder *debugForwader
	closing   uint32
}

func (f *debugForwader) Init() error {
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
			select {
			case <-f.quit:
				close(f.exited)
				return
			default:
				ln.(*net.TCPListener).SetDeadline(time.Now().Add(250 * time.Millisecond))
				c, err := ln.Accept()
				if err != nil {
					if os.IsTimeout(err) {
						break
					}
					f.log.Error(err, "failed to accept debug listener connection, terminate loop")
					return
				}
				connCount := atomic.LoadUint32(&f.connCount)
				if debugForwarderMaxConn <= connCount {
					f.log.WithFields(cue.Fields{"remote_addr": c.RemoteAddr().String(), "connections": connCount}).Warnf("max debug connections reached, dropping new connection")
					_, _ = c.Write([]byte(fmt.Sprintf("max debug connections reached (%d)\n", connCount)))
					_ = c.Close()
					break
				}
				f.log.WithFields(cue.Fields{"remote_addr": c.RemoteAddr().String()}).Info("debug connection opened")
				atomic.AddUint32(&f.connCount, 1)
				dc := &debugConn{
					Conn:      c,
					forwarder: f,
					msgs:      make(chan []byte, 1024),
				}
				dc.closeWait.Add(1)
				f.connsLock.Lock()
				f.conns[c.RemoteAddr().String()] = dc
				f.connsLock.Unlock()
				go dc.loop()
			}
		}
	}(ln)
	return nil
}

func (f *debugForwader) Shutdown(os.Signal) {
	if f == nil {
		return
	}
	atomic.StoreUint32(&f.closing, 1)
	close(f.quit)
	<-f.exited
	if f.connLn != nil {
		f.connLn.Close()
	}
	f.connsLock.Lock()
	// a bit ugly but closeAndWait tries to lock the map as well in remove
	var toClose []*debugConn
	for k, c := range f.conns {
		toClose = append(toClose, c)
		f.removeUnsafe(k)
	}
	f.connsLock.Unlock()
	for _, c := range toClose {
		c.closeAndWait()
	}
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
	if atomic.LoadUint32(&f.closing) != 0 {
		return
	}
	f.connsLock.RLock()

	for _, c := range f.conns {
		select {
		case c.msgs <- data:
		default:
			// TODO: log that debug conn can't keep up with the speed
		}
	}
	f.connsLock.RUnlock()
}

func (f *debugForwader) removeUnsafe(k string) {
	if _, found := f.conns[k]; found {
		f.log.WithFields(cue.Fields{"source": k}).Info("debug connection closed")
		delete(f.conns, k)
		atomic.AddUint32(&f.connCount, ^uint32(0))
	}
}

func (f *debugForwader) remove(k string) {
	f.connsLock.Lock()
	f.removeUnsafe(k)
	f.connsLock.Unlock()
}

func (c *debugConn) Close() {
	c.o.Do(func() { close(c.msgs) })
}

func (c *debugConn) closeAndWait() {
	c.Close()
	c.closeWait.Wait()
}

func (c *debugConn) loop() {
	defer c.closeWait.Done()
	defer c.Conn.Close()
	defer c.forwarder.remove(c.RemoteAddr().String())
	for {
		data, ok := <-c.msgs
		if !ok {
			return
		}
		if len(data) == 0 {
			continue
		}
		err := c.SetWriteDeadline(time.Now().Add(time.Second))
		if err == nil {
			_, err = c.Write(data)
		}
		if err != nil {
			c.forwarder.log.WithFields(cue.Fields{"error": err}).Info("debug connection forwarding failed, terminate")
			return
		}
	}
}
