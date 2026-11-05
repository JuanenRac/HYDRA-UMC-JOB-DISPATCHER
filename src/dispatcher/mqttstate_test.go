// HYDRA-UMC-JOB-DISPATCHER - tests of the publication of the state of the queue
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func validStateConfig() MissionStateConfig {
	return MissionStateConfig{Enabled: true, URL: "mqtt://127.0.0.1:1883", Topic: StateTopic, Interval: 5 * time.Second, Refresh: 60 * time.Second}
}

func TestMissionStateConfig_Validate(t *testing.T) {
	if err := (MissionStateConfig{}).Validate(); err != nil {
		t.Errorf("off must validate: %v", err)
	}
	if err := validStateConfig().Validate(); err != nil {
		t.Errorf("valid: %v", err)
	}
	bad := map[string]func(*MissionStateConfig){
		"remote plain":  func(c *MissionStateConfig) { c.URL = "mqtt://broker.example:1883" },
		"credentials":   func(c *MissionStateConfig) { c.URL = "mqtts://user:pw@broker.example" },
		"wrong scheme":  func(c *MissionStateConfig) { c.URL = "http://127.0.0.1" },
		"wildcard":      func(c *MissionStateConfig) { c.Topic = "hydra/#" },
		"empty topic":   func(c *MissionStateConfig) { c.Topic = "" },
		"fast interval": func(c *MissionStateConfig) { c.Interval = time.Millisecond },
		"short refresh": func(c *MissionStateConfig) { c.Refresh = time.Second },
	}
	for name, mutate := range bad {
		c := validStateConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	remote := validStateConfig()
	remote.URL = "mqtts://broker.example:8883"
	if err := remote.Validate(); err != nil {
		t.Errorf("mqtts to another host must be accepted: %v", err)
	}
}

func TestMQTTPackets(t *testing.T) {
	pub := mqttPublishPacket("a/b", []byte("hi"))
	want := []byte{0x31, 0x07, 0x00, 0x03, 'a', '/', 'b', 'h', 'i'}
	if !bytes.Equal(pub, want) {
		t.Errorf("publish = %v, want %v", pub, want)
	}
	if got := mqttRemainingLength(321); !bytes.Equal(got, []byte{0xc1, 0x02}) {
		t.Errorf("remaining length 321 = %v", got)
	}
	c := mqttConnectPacket("id", "u", "p")
	if c[0] != 0x10 || !bytes.Contains(c, []byte("MQTT")) || c[len(c)-1] != 'p' {
		t.Errorf("connect packet = %v", c)
	}
	if anon := mqttConnectPacket("id", "", ""); bytes.Contains(anon, []byte("p")) && anon[9] != 0x02 {
		t.Errorf("an anonymous connect must carry only the clean session flag: %v", anon)
	}
}

func TestStatePublisher_PublishesOnChangeAndOnRefresh(t *testing.T) {
	e := NewEngine()
	var sent [][]byte
	p, err := NewStatePublisher(e, validStateConfig(), func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	p.publish = func(_ MissionStateConfig, payload []byte) error {
		sent = append(sent, payload)
		return nil
	}
	now := time.Unix(1000, 0)
	last, at := p.Tick(nil, time.Time{}, now)
	if len(sent) != 1 || !strings.Contains(string(last), StateSchema) {
		t.Fatalf("the first tick must publish: %d %s", len(sent), last)
	}
	last, at = p.Tick(last, at, now.Add(10*time.Second))
	if len(sent) != 1 {
		t.Fatal("an unchanged state inside the refresh time must not be published again")
	}
	if err := e.AddJob(Job{ID: "j"}); err != nil {
		t.Fatal(err)
	}
	last, at = p.Tick(last, at, now.Add(20*time.Second))
	if len(sent) != 2 {
		t.Fatal("a changed state must be published")
	}
	_, _ = p.Tick(last, at, now.Add(100*time.Second))
	if len(sent) != 3 {
		t.Fatal("the state must be published again after the refresh time")
	}
}

func TestStatePublisher_ABrokerThatFailsKeepsTheOldState(t *testing.T) {
	p, _ := NewStatePublisher(NewEngine(), validStateConfig(), func(string, ...any) {})
	p.publish = func(MissionStateConfig, []byte) error { return bytes.ErrTooLarge }
	last, at := p.Tick(nil, time.Time{}, time.Unix(1, 0))
	if last != nil || !at.IsZero() {
		t.Fatal("a failed publication must not count as published")
	}
}
