package speech

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

var errUnpronounceable = errors.New("cannot pronounce this English word")

var g2pHashes = map[string]string{
	"g2p-us.onnx": "8db6296406d56cf35b8ca528ca50c09b5fcef78bf805f99e3f7802e311359889",
	"g2p-us.json": "25099369360a6945ba1d64eadd1976ddbb3fe7c94fbb5b4b4efc5e5cf804f6b7",
	"g2p-gb.onnx": "4dbd23e10d7b0ebfc56989724677462b7c352369dfdadf8f7dd526ddaf40c70b",
	"g2p-gb.json": "3d09698d38145b94fc92fefabc661397aa478a2a682bde063aac6459f294d630",
}

type g2pMetadata struct {
	Format    int    `json:"format"`
	Graphemes string `json:"graphemes"`
	Phonemes  string `json:"phonemes"`
	BOS       int64  `json:"bos_id"`
	EOS       int64  `json:"eos_id"`
	Start     int64  `json:"start_id"`
	MaxSteps  int    `json:"max_steps"`
}
type neuralG2P struct {
	path      string
	meta      g2pMetadata
	graphemes map[rune]int64
	phones    []rune
	session   *ort.DynamicAdvancedSession
	cache     map[string]string
}

func newG2P(dir string, british bool) (*neuralG2P, error) {
	accent := "us"
	if british {
		accent = "gb"
	}
	base := "g2p-" + accent
	for _, ext := range []string{".onnx", ".json"} {
		name := base + ext
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != g2pHashes[name] {
			return nil, fmt.Errorf("Pronunciation asset failed SHA-256 verification: %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, base+".json"))
	if err != nil {
		return nil, err
	}
	g := &neuralG2P{path: filepath.Join(dir, base+".onnx"), graphemes: map[rune]int64{}, cache: map[string]string{}}
	if err = json.Unmarshal(data, &g.meta); err != nil {
		return nil, err
	}
	if g.meta.Format != 1 || g.meta.MaxSteps < 4 || g.meta.MaxSteps > 64 || g.meta.BOS != 1 || g.meta.EOS != 2 || g.meta.Start != 1 {
		return nil, errors.New("Unsupported pronunciation model metadata format")
	}
	g.phones = []rune(g.meta.Phonemes)
	for i, r := range []rune(g.meta.Graphemes) {
		if i > 3 {
			g.graphemes[r] = int64(i)
		}
	}
	return g, nil
}
func (g *neuralG2P) phonemize(ctx context.Context, word string) (string, error) {
	if value, ok := g.cache[word]; ok {
		return value, nil
	}
	normalized := []rune(asciiWord(strings.NewReplacer("’", "'", "‘", "'").Replace(word)))
	if len(normalized) == 0 || len(normalized) > g.meta.MaxSteps-2 {
		return "", fmt.Errorf("%w: Cannot pronounce word of unsupported length", errUnpronounceable)
	}
	ids := []int64{g.meta.BOS}
	for _, r := range normalized {
		id, ok := g.graphemes[r]
		if !ok {
			return "", fmt.Errorf("%w: Cannot pronounce unsupported English word", errUnpronounceable)
		}
		ids = append(ids, id)
	}
	ids = append(ids, g.meta.EOS)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if g.session == nil {
		var err error
		g.session, err = newSession(g.path, []string{"input_ids", "decoder_input_ids"}, []string{"logits"}, 2)
		if err != nil {
			return "", err
		}
	}
	input, err := ort.NewTensor(ort.NewShape(1, int64(len(ids))), ids)
	if err != nil {
		return "", err
	}
	defer input.Destroy()
	decoder := []int64{g.meta.Start}
	finished := false
	for step := 0; step < g.meta.MaxSteps-1; step++ {
		d, err := ort.NewTensor(ort.NewShape(1, int64(len(decoder))), decoder)
		if err != nil {
			return "", err
		}
		logits, err := runFloat(ctx, g.session, []ort.Value{input, d})
		d.Destroy()
		if err != nil {
			return "", err
		}
		width := len(logits) / len(decoder)
		// The exported checkpoints pad their output vocabulary to 63 IDs.
		// Only the named phonemes are accepted after decoding.
		if len(logits) != len(decoder)*width || width < len(g.phones) || width > 1024 {
			return "", errors.New("Invalid pronunciation model output shape")
		}
		last := logits[len(logits)-width:]
		token := 0
		for i, v := range last {
			if v > last[token] {
				token = i
			}
		}
		decoder = append(decoder, int64(token))
		if int64(token) == g.meta.EOS {
			finished = true
			break
		}
	}
	if !finished {
		return "", fmt.Errorf("%w: Pronunciation model did not finish word", errUnpronounceable)
	}
	decoded := decoder[1 : len(decoder)-1]
	if len(decoded) > 0 && decoded[0] == g.meta.BOS {
		decoded = decoded[1:]
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("%w: Pronunciation model could not pronounce word", errUnpronounceable)
	}
	var out strings.Builder
	for _, id := range decoded {
		if id <= 3 || id >= int64(len(g.phones)) {
			return "", fmt.Errorf("%w: Pronunciation model could not pronounce word", errUnpronounceable)
		}
		out.WriteRune(g.phones[id])
	}
	phones := strings.NewReplacer("ɾ", "T", "ʔ", "t").Replace(out.String())
	if len(g.cache) >= 4096 {
		clear(g.cache)
	}
	g.cache[word] = phones
	return phones, nil
}
func (g *neuralG2P) close() error {
	if g.session != nil {
		return g.session.Destroy()
	}
	return nil
}
