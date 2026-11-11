package speech

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func loadVocabulary(path string) (map[rune]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vocab := map[rune]int64{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		at := strings.LastIndexByte(line, ' ')
		if at < 0 {
			return nil, errors.New("Invalid speech vocabulary.")
		}
		runes := []rune(line[:at])
		id, err := strconv.ParseInt(line[at+1:], 10, 64)
		if err != nil || len(runes) != 1 || id < 0 {
			return nil, errors.New("Invalid speech vocabulary.")
		}
		vocab[runes[0]] = id
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(vocab) == 0 {
		return nil, errors.New("Empty speech vocabulary.")
	}
	return vocab, nil
}
func loadStyles(path string) (map[string][]float32, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open Kokoro voice archive: %w", err)
	}
	defer archive.Close()
	wanted := map[string]bool{}
	for _, v := range voiceNames {
		wanted[v[0]+".npy"] = true
	}
	styles := map[string][]float32{}
	for _, entry := range archive.File {
		if !wanted[entry.Name] {
			continue
		}
		if entry.UncompressedSize64 > 510*256*4+65536 {
			return nil, errors.New("Invalid Kokoro voice archive entry size.")
		}
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(r, 510*256*4+65537))
		r.Close()
		if readErr != nil {
			return nil, readErr
		}
		values, err := parseVoiceNPY(data)
		if err != nil {
			return nil, fmt.Errorf("invalid voice %s: %w", entry.Name, err)
		}
		key := strings.TrimSuffix(entry.Name, ".npy")
		if _, exists := styles[key]; exists {
			return nil, errors.New("Duplicate Kokoro voice archive entry.")
		}
		styles[key] = values
	}
	if len(styles) != len(voiceNames) {
		return nil, errors.New("The Kokoro voice archive is missing voices.")
	}
	return styles, nil
}

var dtypeRE = regexp.MustCompile(`['"]descr['"]\s*:\s*['"]([<>=|])f4['"]`)
var shapeRE = regexp.MustCompile(`['"]shape['"]\s*:\s*\(\s*510\s*,\s*1\s*,\s*256\s*,?\s*\)`)
var cOrderRE = regexp.MustCompile(`['"]fortran_order['"]\s*:\s*False`)

// Only the finite, C-contiguous float32 array format used by Kokoro is accepted.
// This is a data parser: unlike Python pickle loading, headers cannot execute code.
func parseVoiceNPY(data []byte) ([]float32, error) {
	if len(data) < 10 || string(data[:6]) != "\x93NUMPY" {
		return nil, errors.New("invalid NPY header")
	}
	offset, headerLength := 10, int(binary.LittleEndian.Uint16(data[8:10]))
	if data[6] == 2 || data[6] == 3 {
		if len(data) < 12 {
			return nil, errors.New("short NPY header")
		}
		offset = 12
		headerLength = int(binary.LittleEndian.Uint32(data[8:12]))
	} else if data[6] != 1 {
		return nil, errors.New("unsupported NPY version")
	}
	if headerLength > 65536 || headerLength < 0 || offset+headerLength > len(data) {
		return nil, errors.New("invalid NPY header length")
	}
	header := string(data[offset : offset+headerLength])
	dtype := dtypeRE.FindStringSubmatch(header)
	if len(dtype) != 2 || !shapeRE.MatchString(header) || !cOrderRE.MatchString(header) {
		return nil, errors.New("expected finite float32 voice shape (510,1,256)")
	}
	raw := data[offset+headerLength:]
	if len(raw) != 510*256*4 {
		return nil, errors.New("invalid NPY data size")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if dtype[1] == ">" {
		order = binary.BigEndian
	}
	result := make([]float32, 510*256)
	for i := range result {
		result[i] = math.Float32frombits(order.Uint32(raw[i*4:]))
		if math.IsNaN(float64(result[i])) || math.IsInf(float64(result[i]), 0) {
			return nil, errors.New("nonfinite voice style")
		}
	}
	return result, nil
}
