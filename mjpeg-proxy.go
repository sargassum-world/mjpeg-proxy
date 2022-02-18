/*
 * mjpeg-proxy -- Republish a MJPEG HTTP image stream using a server in Go
 *
 * Copyright (C) 2015-2020, Valentin Vidic
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

var (
	clientHeader  string
	frameTimeout  time.Duration
	stopDelay     time.Duration
	tcpSendBuffer int
)

type configSource struct {
	Source   string
	Username string
	Password string
	Digest   bool
	Path     string
	Rate     float64
}

func startSource(source, username, password, proxyUrl string, digest bool, rate float64) error {
	chunker, err := NewChunker(proxyUrl, source, username, password, digest, rate)
	if err != nil {
		return fmt.Errorf("chunker[%s]: create failed: %s", proxyUrl, err)
	}
	pubSub := NewPubSub(proxyUrl, chunker)
	pubSub.Start()

	fmt.Printf("chunker[%s]: serving from %s\n", proxyUrl, source)
	http.Handle(proxyUrl, pubSub)

	return nil
}

func loadConfig(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() {
		err := file.Close()
		if err != nil {
			fmt.Printf("config: file close failed for %s: %s\n", file.Name(), err)
		}
	}()

	sources := make([]configSource, 0)
	dec := json.NewDecoder(file)
	err = dec.Decode(&sources)
	if err != nil && err != io.EOF {
		return err
	}

	exists := make(map[string]bool)
	for _, conf := range sources {
		if exists[conf.Path] {
			return fmt.Errorf("duplicate proxy path: %s", conf.Path)
		}

		err = startSource(conf.Source, conf.Username, conf.Password, conf.Path, conf.Digest, conf.Rate)
		if err != nil {
			return err
		}

		exists[conf.Path] = true
	}

	return nil
}

func connStateEvent(conn net.Conn, event http.ConnState) {
	if event == http.StateActive && tcpSendBuffer > 0 {
		switch c := conn.(type) {
		case *net.TCPConn:
			c.SetWriteBuffer(tcpSendBuffer)
		case *net.UnixConn:
			c.SetWriteBuffer(tcpSendBuffer)
		}
	}
}

func unixListen(path string) (net.Listener, error) {
	fi, err := os.Stat(path)
	if !os.IsNotExist(err) && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path)
	}

	return net.Listen("unix", path)
}

func listenAndServe(addr string) error {
	var listener net.Listener
	var err error

	if strings.HasPrefix(addr, "unix:") {
		listener, err = unixListen(strings.TrimPrefix(addr, "unix:"))
	} else {
		listener, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return err
	}

	fmt.Printf("server: starting on address %s\n", addr)
	server := &http.Server{
		ConnState: connStateEvent,
	}
	return server.Serve(listener)
}

func lookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func main() {
	// TODO: allow setting all flags from env vars, not just the string flags
	source := flag.String("source", lookupEnvOrString("SOURCE_URL", "http://example.com/img.mjpg"), "source uri")
	username := flag.String("username", lookupEnvOrString("SOURCE_USERNAME", ""), "source uri username")
	password := flag.String("password", lookupEnvOrString("SOURCE_PASSWORD", ""), "source uri password")
	digest := flag.Bool("digest", false, "source uri uses digest authentication")
	sources := flag.String("sources", lookupEnvOrString("SOURCES_JSON", ""), "JSON configuration file to load sources from")
	bind := flag.String("bind", lookupEnvOrString("SERVE_BIND", ":8080"), "proxy bind address")
	path := flag.String("path", lookupEnvOrString("SERVE_PATH", "/"), "proxy serving path")
	rate := flag.Float64("rate", 0, "limit output frame rate")
	maxprocs := flag.Int("maxprocs", 0, "limit number of CPUs used")
	flag.DurationVar(&frameTimeout, "frametimeout", 60*time.Second, "limit waiting for next frame")
	flag.DurationVar(&stopDelay, "stopduration", 60*time.Second, "follow source after last client")
	flag.IntVar(&tcpSendBuffer, "sendbuffer", 4096, "limit buffering of frames")
	flag.StringVar(&clientHeader, "clientheader", "", "request header with client address")
	flag.Parse()

	if *maxprocs > 0 {
		runtime.GOMAXPROCS(*maxprocs)
	}

	var err error
	if *sources != "" {
		err = loadConfig(*sources)
	} else {
		err = startSource(*source, *username, *password, *path, *digest, *rate)
	}
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}

	err = listenAndServe(*bind)
	if err != nil {
		fmt.Println("server:", err)
		os.Exit(1)
	}
}
