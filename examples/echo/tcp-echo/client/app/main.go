/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	// "flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	// "strings"
	"sync/atomic"
	"syscall"
	"time"
)

import (
	getty "github.com/apache/dubbo-getty"
	"github.com/dubbogo/gost/net"
	"github.com/dubbogo/gost/sync"
	"github.com/dubbogo/gost/time"
)

const (
	pprofPath = "/debug/pprof/"
)

const (
	WritePkgTimeout = 1e8
)

var (
	client   EchoClient
	taskPool gxsync.GenericTaskPool
)

////////////////////////////////////////////////////////////////////
// main
////////////////////////////////////////////////////////////////////

func main() {
	initConf()

	initProfiling()

	taskPool = gxsync.NewTaskPoolSimple(0)
	initClient()
	log.Infof("%s starts successfull!", conf.AppName)

	go test()

	initSignal()
}

func initProfiling() {
	var addr string

	addr = gxnet.HostAddress(conf.LocalHost, conf.ProfilePort)
	log.Infof("App Profiling startup on address{%v}", addr+pprofPath)
	go func() {
		log.Info(http.ListenAndServe(addr, nil))
	}()
}

func newSession(session getty.Session) error {
	var (
		ok      bool
		tcpConn *net.TCPConn
	)

	if conf.GettySessionParam.CompressEncoding {
		session.SetCompressType(getty.CompressZip)
	}

	if tcpConn, ok = session.Conn().(*net.TCPConn); !ok {
		panic(fmt.Sprintf("%s, session.conn{%#v} is not tcp connection\n", session.Stat(), session.Conn()))
	}

	tcpConn.SetNoDelay(conf.GettySessionParam.TcpNoDelay)
	tcpConn.SetKeepAlive(conf.GettySessionParam.TcpKeepAlive)
	if conf.GettySessionParam.TcpKeepAlive {
		tcpConn.SetKeepAlivePeriod(conf.GettySessionParam.keepAlivePeriod)
	}
	tcpConn.SetReadBuffer(conf.GettySessionParam.TcpRBufSize)
	tcpConn.SetWriteBuffer(conf.GettySessionParam.TcpWBufSize)

	session.SetName(conf.GettySessionParam.SessionName)
	session.SetMaxMsgLen(conf.GettySessionParam.MaxMsgLen)
	session.SetPkgHandler(echoPkgHandler)
	session.SetEventListener(echoMsgHandler)
	session.SetReadTimeout(conf.GettySessionParam.tcpReadTimeout)
	session.SetWriteTimeout(conf.GettySessionParam.tcpWriteTimeout)
	session.SetCronPeriod((int)(conf.heartbeatPeriod.Nanoseconds() / 1e6))
	session.SetWaitTime(conf.GettySessionParam.waitTimeout)
	log.Debugf("client new session:%s", session.Stat())

	return nil
}

func initClient() {
	clientOpts := []getty.ClientOption{getty.WithServerAddress(gxnet.HostAddress(conf.ServerHost, conf.ServerPort))}
	clientOpts = append(clientOpts, getty.WithClientTaskPool(taskPool))
	clientOpts = append(clientOpts, getty.WithReconnectAttempts(conf.GettySessionParam.TcpMaxReconnectAttempts))
	if conf.ConnectionNum != 0 {
		clientOpts = append(clientOpts, getty.WithConnectionNumber(conf.ConnectionNum))
	}

	client.gettyClient = getty.NewTCPClient(clientOpts...)
	client.gettyClient.RunEventLoop(newSession)
}

func uninitClient() {
	client.close()
	if taskPool != nil {
		taskPool.Close()
	}
}

func initSignal() {
	// signal.Notify的ch信道是阻塞的(signal.Notify不会阻塞发送信号), 需要设置缓冲
	signals := make(chan os.Signal, 1)
	// It is not possible to block SIGKILL or syscall.SIGSTOP
	signal.Notify(signals, os.Interrupt, os.Kill, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	for {
		sig := <-signals
		log.Infof("get signal %s", sig.String())
		switch sig {
		case syscall.SIGHUP:
		// reload()
		default:
			go time.AfterFunc(conf.failFastTimeout, func() {
				// log.Warn("app exit now by force...")
				// os.Exit(1)
			})

			// 要么fastFailTimeout时间内执行完毕下面的逻辑然后程序退出，要么执行上面的超时函数程序强行退出
			uninitClient()
			// fmt.Println("app exit now...")
			return
		}
	}
}

func echo() {
	var pkg EchoPackage
	pkg.H.Magic = echoPkgMagic
	pkg.H.LogID = (uint32)(src.Int63())
	pkg.H.Sequence = atomic.AddUint32(&reqID, 1)
	// pkg.H.ServiceID = 0
	pkg.H.Command = echoCmd
	pkg.B = conf.EchoString
	pkg.H.Len = (uint16)(len(pkg.B)) + 1

	if session := client.selectSession(); session != nil {
		_, _, err := session.WritePkg(&pkg, WritePkgTimeout)
		if err != nil {
			log.Warn("session.WritePkg(session{%s}, pkg{%s}) = error{%v}", session.Stat(), pkg, err)
			session.Close()
			client.removeSession(session)
		}
	}
}

func test() {
	for {
		if client.isAvailable() {
			break
		}
		time.Sleep(1e6)
	}

	var (
		cost    int64
		counter gxtime.CountWatch
	)
	counter.Start()
	for i := 0; i < conf.EchoTimes; i++ {
		echo()
	}
	cost = counter.Count()
	log.Infof("after loop %d times, echo cost %d ms", conf.EchoTimes, cost/1e6)
}
