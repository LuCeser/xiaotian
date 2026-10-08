//go:build ignore

package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xiaotian/home-tutor-agent/pkg/crc16"
)

func buildFrame(msgType uint8, payload []byte) []byte {
	header := make([]byte, 16)
	header[0] = 0x01
	header[1] = msgType
	binary.BigEndian.PutUint32(header[2:6], 1)
	binary.BigEndian.PutUint32(header[6:10], uint32(time.Now().UnixMicro()&0xFFFFFFFF))
	binary.BigEndian.PutUint16(header[10:12], uint16(len(payload)))
	binary.BigEndian.PutUint16(header[12:14], 0)
	crc := crc16.Checksum(header[0:14])
	binary.BigEndian.PutUint16(header[14:16], crc)
	return append(header, payload...)
}

func main() {
	h := http.Header{}
	h.Set("Origin", "http://localhost")
	c, _, err := websocket.DefaultDialer.Dial("ws://localhost:8080/ws", h)
	if err != nil {
		fmt.Println("DIAL_ERR:", err)
		os.Exit(1)
	}
	defer c.Close()
	fmt.Println("WS connected")

	send := func(m map[string]interface{}) {
		b, _ := json.Marshal(m)
		if err := c.WriteMessage(websocket.BinaryMessage, buildFrame(0x02, b)); err != nil {
			fmt.Println("WRITE_ERR:", err)
			os.Exit(1)
		}
	}

	// --- clear fetch locks first (cmd_due locks fetched cards for FETCH_LOCK_HOURS) ---
	send(map[string]interface{}{"type": "unlock_cards", "params": map[string]interface{}{"category": ""}})
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			break
		}
		if len(msg) >= 16 {
			pl := binary.BigEndian.Uint16(msg[10:12])
			fmt.Printf("unlock resp type=0x%02x payload=%s\n", msg[1], string(msg[16:16+int(pl)]))
		}
		break
	}

	// --- T2: start_session ---
	send(map[string]interface{}{"type": "start_session", "params": map[string]interface{}{"limit": 5}})

	var sessionID string
	var words []string
	deadline := time.Now().Add(45 * time.Second)
	c.SetReadDeadline(deadline)
	t2ok := false
	for !t2ok && time.Now().Before(deadline) {
		_, msg, err := c.ReadMessage()
		if err != nil {
			fmt.Println("READ_ERR_T2:", err)
			break
		}
		if len(msg) < 16 {
			continue
		}
		ft := msg[1]
		pl := binary.BigEndian.Uint16(msg[10:12])
		payload := msg[16 : 16+int(pl)]
		fmt.Printf("T2 frame type=0x%02x len=%d payload=%s\n", ft, pl, string(payload))
		var p map[string]interface{}
		if json.Unmarshal(payload, &p) == nil {
			if p["type"] == "session_ready" {
				t2ok = true
				sessionID, _ = p["session_id"].(string)
				if w, ok := p["words"].([]interface{}); ok {
					for _, x := range w {
						words = append(words, fmt.Sprint(x))
					}
				}
			}
			if p["type"] == "error" {
				fmt.Println("T2 ERROR FRAME:", string(payload))
				os.Exit(2)
			}
		}
	}
	if !t2ok {
		fmt.Println("T2_RESULT: FAIL (no session_ready)")
		os.Exit(2)
	}
	fmt.Printf("T2_RESULT: PASS session_id=%s words=%v\n", sessionID, words)
	if len(words) == 0 {
		fmt.Println("ABORT: no words to play")
		os.Exit(2)
	}

	// --- T3+T4: play_word, measure TTF, count frames ---
	wordID := words[0]
	playSent := time.Now()
	send(map[string]interface{}{"type": "play_word", "params": map[string]interface{}{"session_id": sessionID, "word_id": wordID}})

	audioCount := 0
	audioBytes := 0
	var ttf time.Duration
	wordComplete := false
	deadline2 := time.Now().Add(30 * time.Second)
	c.SetReadDeadline(deadline2)
	for time.Now().Before(deadline2) {
		_, msg, err := c.ReadMessage()
		if err != nil {
			fmt.Println("READ_ERR_T3:", err)
			break
		}
		if len(msg) < 16 {
			continue
		}
		ft := msg[1]
		pl := binary.BigEndian.Uint16(msg[10:12])
		if ft == 0x01 { // audio
			audioCount++
			audioBytes += int(pl)
			if ttf == 0 {
				ttf = time.Since(playSent)
			}
		} else {
			payload := msg[16 : 16+int(pl)]
			var p map[string]interface{}
			json.Unmarshal(payload, &p)
			fmt.Printf("T3 ctrl frame type=0x%02x payload=%s\n", ft, string(payload))
			if p["type"] == "word_complete" {
				wordComplete = true
				break
			}
		}
	}

	fmt.Printf("T3_RESULT: word=%s audio_frames=%d audio_bytes=%d word_complete=%v\n", wordID, audioCount, audioBytes, wordComplete)
	fmt.Printf("T4_RESULT: time_to_first_audio=%v threshold=1.5s\n", ttf)
	if ttf > 0 && ttf < 1500*time.Millisecond {
		fmt.Println("T4_VERDICT: PASS (<1.5s)")
	} else {
		fmt.Println("T4_VERDICT: FAIL (>=1.5s)")
	}
}
