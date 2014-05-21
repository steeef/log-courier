package main

import (
  "errors"
  "fmt"
  "regexp"
  "strings"
  "sync"
  "time"
)

const codecMultiline_What_Previous = 0x00000001
const codecMultiline_What_Next = 0x00000002

type CodecMultilineFactory struct {
  pattern          string
  what             int
  negate           bool
  previous_timeout time.Duration

  matcher *regexp.Regexp
}

type CodecMultiline struct {
  config      *CodecMultilineFactory
  harvester   *Harvester
  output      chan *FileEvent
  last_offset int64

  h_offset    int64
  offset      int64
  line        uint64
  buffer      []string
  timer_lock  *sync.Mutex
  timer_chan  chan bool
}

func CreateCodecMultilineFactory(config map[string]interface{}) (*CodecMultilineFactory, error) {
  var ok bool
  result := &CodecMultilineFactory{}
  for key, value := range config {
    if key == "name" {
    } else if key == "pattern" {
      result.pattern, ok = value.(string)
      if !ok {
        return nil, errors.New("Invalid value for 'pattern'. Must be a string.")
      }
      var err error
      result.matcher, err = regexp.Compile(result.pattern)
      if err != nil {
        return nil, errors.New(fmt.Sprintf("Failed to compile multiline codec pattern, '%s'.", err))
      }
    } else if key == "what" {
      var what string
      what, ok = value.(string)
      if !ok {
        return nil, errors.New("Invalid value for 'what'. Must be a string.")
      }
      if what == "previous" {
        result.what = codecMultiline_What_Previous
      } else if what == "next" {
        result.what = codecMultiline_What_Next
      } else {
        return nil, errors.New("Invalid value for 'what'. Must be either 'previous' or 'next'.")
      }
    } else if key == "negate" {
      result.negate, ok = value.(bool)
      if !ok {
        return nil, errors.New("Invalid value for 'negate'. Must be true or false.")
      }
    } else if key == "previous_timeout" {
      previous_timeout, ok := value.(string)
      if !ok {
        return nil, errors.New("Invalid value for 'previous_timeout'. Must be a string duration.")
      }
      var err error
      result.previous_timeout, err = time.ParseDuration(previous_timeout)
      if err != nil {
        return nil, errors.New(fmt.Sprintf("Invalid value for 'previous_timeout'. Failed to parse duration: %s.", err))
      }
    } else {
      return nil, errors.New(fmt.Sprintf("Unknown multiline codec property, '%s'.", key))
    }
  }
  if result.pattern == "" {
    return nil, errors.New("Multiline codec pattern must be specified.")
  }
  if result.what == 0 {
    result.what = codecMultiline_What_Previous
  }
  return result, nil
}

func (cf *CodecMultilineFactory) Create(harvester *Harvester, output chan *FileEvent) Codec {
  c := &CodecMultiline{config: cf, harvester: harvester, output: output, last_offset: harvester.Offset}

  if cf.previous_timeout != 0 {
    c.timer_lock = new(sync.Mutex)
    c.timer_chan = make(chan bool, 1)

    go func() {
      var active bool

      timer := time.NewTimer(0)

      for {
        select {
        case shutdown := <-c.timer_chan:
          timer.Stop()
          if shutdown {
            // Shutdown signal so end the routine
            break
          }
          timer.Reset(c.config.previous_timeout)
          active = true
        case <-timer.C:
          if active {
            // Surround flush in mutex to prevent data getting modified by a new line while we flush
            c.timer_lock.Lock()
            c.flush()
            c.timer_lock.Unlock()
            active = false
          }
        }
      }
    }()
  }
  return c
}

func (c *CodecMultiline) Teardown() int64 {
  return c.last_offset
}

func (c *CodecMultiline) Event(offset int64, line uint64, text *string) {
  // TODO(driskell): If we are using previous and we match on the very first line read,
  // then this is because we've started in the middle of a multiline event (the first line
  // should never match) - so we could potentially offer an option to discard this.
  // The benefit would be that when using previous_timeout, we could discard any extraneous
  // event data that did not get written in time, if the user so wants it, in order to prevent
  // odd incomplete data. It would be a signal from the user, "I will worry about the buffering
  // issues my programs may have - you just make sure to write each event either completely or
  // partially, always with the FIRST line correct (which could be the important one)."
  match_failed := c.config.negate == c.config.matcher.MatchString(*text)
  if c.config.what == codecMultiline_What_Previous {
    if c.config.previous_timeout != 0 {
      // Prevent a flush happening while we're modifying the stored data
      c.timer_lock.Lock()
    }
    if match_failed {
      c.flush()
    }
  }
  if len(c.buffer) == 0 {
    c.line = line
    c.offset = offset
  }
  c.h_offset = c.harvester.Offset
  c.buffer = append(c.buffer, *text)
  if c.config.what == codecMultiline_What_Previous {
    if c.config.previous_timeout != 0 {
      // Reset the timer and unlock
      c.timer_chan <- false
      c.timer_lock.Unlock()
    }
  } else if c.config.what == codecMultiline_What_Next && match_failed {
    c.flush()
  }
}

func (c *CodecMultiline) flush() {
  if len(c.buffer) == 0 {
    return
  }

  text := strings.Join(c.buffer, "\n")

  event := &FileEvent{
    ProspectorInfo: c.harvester.ProspectorInfo,
    Offset:         c.h_offset,
    Event:          CreateEvent(c.harvester.FileConfig.Fields, &c.harvester.Path, c.offset, c.line, &text),
  }

  c.output <- event // ship the new event downstream

  // Set last offset - this is returned in Teardown so if we're mid multiline and crash, we start this multiline again
  c.last_offset = c.offset
  c.buffer = nil
}