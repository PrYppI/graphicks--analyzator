// Package tonaddr — конвертация TON-адресов между форматами.
// User-friendly (EQ…/UQ…, base64url с CRC) ↔ raw («workchain:hex»).
package tonaddr

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// FriendlyFromRaw конвертирует «0:hex…» в user-friendly EQ… (bounceable,
// basechain) — ровно в том виде, в каком пулы и токены фигурируют на
// GeckoTerminal. Формат: 1 байт тег (0x11), 1 байт workchain, 32 байта
// хеш, 2 байта CRC-16/XMODEM (poly 0x1021) поверх первых 34 байт.
func FriendlyFromRaw(raw string) (string, bool) {
	wcStr, hash, ok := strings.Cut(raw, ":")
	if !ok || len(hash) != 64 {
		return "", false
	}
	wc, err := strconv.Atoi(wcStr)
	if err != nil || (wc != 0 && wc != -1) {
		return "", false
	}
	payload := make([]byte, 0, 36)
	tag := byte(0x51) // non-bounceable
	if wc >= 0 {
		tag = 0x11 // bounceable
	}
	payload = append(payload, tag, byte(wc))
	for i := 0; i < 64; i += 2 {
		hi, e1 := hexVal(hash[i])
		lo, e2 := hexVal(hash[i+1])
		if e1 != nil || e2 != nil {
			return "", false
		}
		payload = append(payload, hi<<4|lo)
	}
	crc := crc16Xmodem(payload)
	payload = append(payload, byte(crc>>8), byte(crc&0xFF))
	return base64.RawURLEncoding.EncodeToString(payload), true
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("не hex-символ: %q", c)
}

// crc16Xmodem — контрольная сумма TON-адресов.
func crc16Xmodem(data []byte) uint16 {
	crc := uint16(0)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
