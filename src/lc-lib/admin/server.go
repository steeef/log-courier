/*
* Copyright 2014 Jason Woods.
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
*
* http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*/

package admin

import (
  "encoding/gob"
  "fmt"
  "io"
  "net"
  "time"
)

type server struct {
  listener *Listener
  conn     *net.TCPConn
  encoder  *gob.Encoder
}

func newServer(listener *Listener,conn *net.TCPConn) *server {
  return &server{
    listener: listener,
    conn:     conn,
  }
}

func (s *server) Run() {
  if err := s.loop(); err != nil {
    log.Warning("Error on admin connection from %s: %s", s.conn.RemoteAddr(), err)
  } else {
    log.Debug("Admin connection from %s closed", s.conn.RemoteAddr())
  }

  s.conn.Close()

  s.listener.conn_group.Done()
}

func (s *server) loop() (err error) {
  var result interface{}

  s.encoder = gob.NewEncoder(s.conn)

  command := make([]byte, 4)

  for {
    if err = s.readCommand(command); err != nil {
      if err == io.EOF {
        err = nil
      }
      return
    }

    log.Debug("Command from %s: %s", s.conn.RemoteAddr(), command)

    if string(command) == "PING" {
      result = &Response{&PongResponse{}}
    } else {
      result = s.processCommand(string(command))
    }

    if err = s.sendResponse(result); err != nil {
      return
    }
  }
}

func (s *server) readCommand(command []byte) error {
  total_read := 0
  start_time := time.Now()

  for {
    if err := s.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
      return err
    }

    read, err := s.conn.Read(command[total_read:4])
    if err != nil {
      if op_err, ok := err.(*net.OpError); ok && op_err.Timeout() {
        if time.Now().Sub(start_time) <= 1800 * time.Second {
          continue
        }
      } else if total_read != 0 && op_err == io.EOF {
        return fmt.Errorf("EOF")
      }
      return err
    }

    total_read += read
    if total_read == 4 {
      break
    }
  }

  return nil
}

func (s *server) sendResponse(response interface{}) error {
  if err := s.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
    return err
  }

  if err := s.encoder.Encode(response); err != nil {
    return err
  }

  return nil
}

func (s *server) processCommand(command string) interface{} {
  select {
  case s.listener.command_chan <- command:
  case <-s.listener.OnShutdown():
    return &ErrorResponse{Message: "Log Courier is shutting down"}
  }

  return <-s.listener.response_chan
}