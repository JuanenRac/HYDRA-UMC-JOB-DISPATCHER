// HYDRA-UMC-JOB-DISPATCHER - the state of the mission queue published as a retained MQTT message
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// StateTopic is where the state of the mission queue (job-dispatch-state/1) is published, retained. It is read-only for every reader:
// NODE-HEALING reads it to know which robots hold a mission before it acts, and nothing published here moves anything.
const StateTopic = "hydra/jobs/state"

// MissionStateConfig is the setting of the "mission_state" section (off when absent).
type MissionStateConfig struct {
	Enabled bool
	// URL of the broker: mqtt://host:port (plain; only accepted for this machine) or mqtts://host:port.
	URL      string
	Topic    string
	User     string
	Password string
	// Interval is how often the state is looked at; it is published when it changed, and again after Refresh even if it did not (so
	// a reader that connected late, or a broker that lost the retained message, gets a current one).
	Interval time.Duration
	Refresh  time.Duration
}

// Validate says why the setting cannot be used (nil when it can, or when it is off).
func (c MissionStateConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "mqtt" && u.Scheme != "mqtts") {
		return errors.New("mission_state.url must be mqtt://host:port or mqtts://host:port, without credentials")
	}
	if u.Scheme == "mqtt" && !isLoopbackHost(u.Hostname()) {
		return errors.New("mission_state.url: plain mqtt:// is only accepted for this machine; use mqtts:// for another host")
	}
	if c.Topic == "" || strings.ContainsAny(c.Topic, "#+") || strings.HasPrefix(c.Topic, "/") || strings.Contains(c.Topic, "//") {
		return errors.New("mission_state.topic must be a topic with no wildcards")
	}
	if c.Interval < time.Second || c.Refresh < c.Interval {
		return errors.New("mission_state: interval_seconds must be at least 1 and refresh_seconds at least the interval")
	}
	return nil
}

func mqttString(s string) []byte {
	out := make([]byte, 2, 2+len(s))
	binary.BigEndian.PutUint16(out, uint16(len(s)))
	return append(out, s...)
}

func mqttRemainingLength(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// mqttConnectPacket is the CONNECT packet of MQTT 3.1.1, clean session, keep alive 30 s.
func mqttConnectPacket(clientID, user, password string) []byte {
	flags := byte(0x02)
	payload := mqttString(clientID)
	if user != "" {
		flags |= 0x80
		payload = append(payload, mqttString(user)...)
		if password != "" {
			flags |= 0x40
			payload = append(payload, mqttString(password)...)
		}
	}
	body := append(mqttString("MQTT"), 0x04, flags, 0x00, 0x1e)
	body = append(body, payload...)
	return append(append([]byte{0x10}, mqttRemainingLength(len(body))...), body...)
}

// mqttPublishPacket is a PUBLISH at QoS 0 with the retain flag.
func mqttPublishPacket(topic string, payload []byte) []byte {
	body := append(mqttString(topic), payload...)
	return append(append([]byte{0x31}, mqttRemainingLength(len(body))...), body...)
}

// publishOnce opens a connection, publishes one retained message and closes it. A connection per message keeps the code free of
// reconnection state; the state changes seldom.
func publishOnce(c MissionStateConfig, payload []byte) error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "mqtts" {
			host = net.JoinHostPort(u.Hostname(), "8883")
		} else {
			host = net.JoinHostPort(u.Hostname(), "1883")
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var conn net.Conn
	if u.Scheme == "mqtts" {
		conn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	} else {
		conn, err = dialer.Dial("tcp", host)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(mqttConnectPacket("hydra-umc-job-dispatcher", c.User, c.Password)); err != nil {
		return err
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return err
	}
	if ack[0] != 0x20 || ack[1] != 0x02 || ack[3] != 0x00 {
		return fmt.Errorf("the broker refused the connection (code %d)", ack[3])
	}
	if _, err := conn.Write(mqttPublishPacket(c.Topic, payload)); err != nil {
		return err
	}
	_, err = conn.Write([]byte{0xe0, 0x00})
	return err
}

// StatePublisher publishes the state of the queue on the topic. Start it once; Stop ends it.
type StatePublisher struct {
	engine  *Engine
	cfg     MissionStateConfig
	publish func(MissionStateConfig, []byte) error
	logf    func(format string, args ...any)
	stop    chan struct{}
	done    chan struct{}
}

// NewStatePublisher builds the publisher; the setting must be valid.
func NewStatePublisher(engine *Engine, cfg MissionStateConfig, logf func(format string, args ...any)) (*StatePublisher, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &StatePublisher{engine: engine, cfg: cfg, publish: publishOnce, logf: logf, stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// Tick looks at the state once and publishes it when it changed since `last` or when `lastAt` is older than the refresh. It returns the
// payload and the time of the last publication, which the caller passes back in.
func (p *StatePublisher) Tick(last []byte, lastAt time.Time, now time.Time) ([]byte, time.Time) {
	payload, err := json.Marshal(p.engine.State())
	if err != nil {
		return last, lastAt
	}
	if string(payload) == string(last) && now.Sub(lastAt) < p.cfg.Refresh {
		return last, lastAt
	}
	if err := p.publish(p.cfg, payload); err != nil {
		p.logf("[job-dispatcher] mission state not published: %v", err)
		return last, lastAt
	}
	return payload, now
}

// Start runs the loop in its own goroutine.
func (p *StatePublisher) Start() {
	go func() {
		defer close(p.done)
		var last []byte
		var lastAt time.Time
		ticker := time.NewTicker(p.cfg.Interval)
		defer ticker.Stop()
		for {
			last, lastAt = p.Tick(last, lastAt, time.Now())
			select {
			case <-p.stop:
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop ends the loop and waits for it.
func (p *StatePublisher) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
}
