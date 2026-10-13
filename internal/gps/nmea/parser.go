package nmea

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const minSentenceAddressLen = 5

// ErrUnsupportedSentence is returned for NMEA sentences that do not carry a position.
var ErrUnsupportedSentence = errors.New("unsupported NMEA sentence")

// ErrInvalidChecksum is returned when a sentence checksum is missing or does not match.
var ErrInvalidChecksum = errors.New("invalid NMEA checksum")

// Message represents a parsed NMEA message.
type Message interface {
	IsValid() bool
	GetLatitude() float64
	GetLongitude() float64
}

// Parse parses an NMEA sentence and returns a Message.
// RMC and GGA sentences from any talker are accepted. Other sentence types return
// ErrUnsupportedSentence. A checksum mismatch returns ErrInvalidChecksum.
func Parse(sentence string) (Message, error) {
	sentence = strings.TrimSpace(sentence)
	if !strings.HasPrefix(sentence, "$") {
		return nil, errors.New("invalid NMEA sentence: missing $ prefix")
	}
	if err := verifyChecksum(sentence); err != nil {
		return nil, err
	}

	star := strings.LastIndex(sentence, "*")
	fields := strings.Split(sentence[1:star], ",")
	if len(fields) < 2 {
		return nil, errors.New("invalid NMEA sentence: too few fields")
	}

	switch sentenceKind(fields[0]) {
	case "GGA":
		return parseGGA(fields)
	case "RMC":
		return parseRMC(fields)
	default:
		return nil, ErrUnsupportedSentence
	}
}

func sentenceKind(address string) string {
	if len(address) < minSentenceAddressLen {
		return ""
	}
	kind := address[len(address)-3:]
	if kind == "RMC" || kind == "GGA" {
		return kind
	}
	return ""
}

func verifyChecksum(sentence string) error {
	star := strings.LastIndex(sentence, "*")
	if star <= 1 || star+3 > len(sentence) {
		return ErrInvalidChecksum
	}
	parsed, err := strconv.ParseUint(sentence[star+1:star+3], 16, 8)
	if err != nil {
		return ErrInvalidChecksum
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= sentence[i]
	}
	if uint64(sum) != parsed {
		return ErrInvalidChecksum
	}
	return nil
}

// parseGGA parses a GGA (fix data) sentence.
// A structurally complete sentence is returned even when the fix is void so the
// caller can tell it apart from an unrelated sentence.
func parseGGA(fields []string) (*GGAMessage, error) {
	if len(fields) < 7 {
		return nil, errors.New("invalid GGA sentence: too few fields")
	}

	msg := &GGAMessage{}
	lat, latErr := parseCoordinate(fields[2], fields[3], true)
	lon, lonErr := parseCoordinate(fields[4], fields[5], false)
	quality, qualErr := strconv.Atoi(fields[6])
	if latErr != nil || lonErr != nil || qualErr != nil || !validGGAQuality(quality) {
		return msg, nil
	}

	msg.latitude = lat
	msg.longitude = lon
	msg.fixQuality = quality
	msg.valid = true
	return msg, nil
}

// validGGAQuality reports GNSS fix qualities that are a real position.
// 1 GPS, 2 differential, 4 RTK fixed, 5 RTK float. Estimated, manual, and
// simulation qualities are not stored.
func validGGAQuality(quality int) bool {
	switch quality {
	case 1, 2, 4, 5:
		return true
	default:
		return false
	}
}

// parseRMC parses an RMC (recommended minimum) sentence.
func parseRMC(fields []string) (*RMCMessage, error) {
	if len(fields) < 7 {
		return nil, errors.New("invalid RMC sentence: too few fields")
	}

	msg := &RMCMessage{}
	if fields[2] != "A" || !validRMCMode(fields) {
		return msg, nil
	}
	lat, latErr := parseCoordinate(fields[3], fields[4], true)
	lon, lonErr := parseCoordinate(fields[5], fields[6], false)
	if latErr != nil || lonErr != nil {
		return msg, nil
	}

	msg.status = "A"
	msg.latitude = lat
	msg.longitude = lon
	msg.valid = true
	return msg, nil
}

// validRMCMode accepts sentences with no mode field and autonomous, differential,
// precise, and RTK mode characters. N, E, M, and S are not a fix.
func validRMCMode(fields []string) bool {
	if len(fields) <= 12 {
		return true
	}
	switch fields[12] {
	case "", "A", "D", "F", "R", "P":
		return true
	default:
		return false
	}
}

// parseCoordinate parses a coordinate in NMEA form.
// Minutes are the two digits immediately before the decimal point. Latitude uses
// the remaining leading digits as DD and longitude as DDD.
func parseCoordinate(coordStr, direction string, latitude bool) (float64, error) {
	dot := strings.IndexByte(coordStr, '.')
	if dot < 2 {
		return 0, errors.New("invalid NMEA coordinate")
	}
	degreesStr := coordStr[:dot-2]
	if degreesStr == "" {
		return 0, errors.New("invalid NMEA coordinate")
	}
	degrees, err := strconv.ParseFloat(degreesStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid NMEA degrees: %w", err)
	}
	minutes, err := strconv.ParseFloat(coordStr[dot-2:], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid NMEA minutes: %w", err)
	}
	if minutes < 0 || minutes >= 60 {
		return 0, errors.New("invalid NMEA minutes")
	}

	decimal := degrees + minutes/60
	if latitude {
		if direction != "N" && direction != "S" {
			return 0, errors.New("invalid NMEA latitude direction")
		}
		if decimal > 90 {
			return 0, errors.New("invalid NMEA latitude")
		}
		if direction == "S" {
			decimal = -decimal
		}
		return decimal, nil
	}
	if direction != "E" && direction != "W" {
		return 0, errors.New("invalid NMEA longitude direction")
	}
	if decimal > 180 {
		return 0, errors.New("invalid NMEA longitude")
	}
	if direction == "W" {
		decimal = -decimal
	}
	return decimal, nil
}
