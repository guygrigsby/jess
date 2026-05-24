// Package gomlx provides an in-process sentence embedder for jess
// memory, backed by the pure-Go gomlx/compute/gobackend ML runtime.
//
// Status: experimental. Tests download ~90MB of model weights and
// take a few seconds to warm up; CI skips them with -short.
package gomlx

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// tokenizer is a minimal WordPiece tokenizer matching the
// HuggingFace BERT-family format. The implementation mirrors the
// inline tokenizer used in the GoMLX bert-base-ner example
// (Apache 2.0) — sentence-encoder vocab files (vocab.txt) work
// the same way, so the same tokenizer covers all-MiniLM-L6-v2
// and most sentence-transformers BERT variants.
//
// What this does NOT cover (yet — file upstream if we need it):
//   - SentencePiece (used by RoBERTa, ALBERT, T5) — those models
//     ship a "tokenizer.json" instead of "vocab.txt" and would
//     need a different parser. all-MiniLM-L6-v2 uses WordPiece.
//   - Truncation to max_length — caller is responsible for keeping
//     inputs under the model's positional limit (128 for MiniLM).
//   - Per-piece normalization (do_lower_case, NFC, etc.) — assumed
//     baked into the vocab; not re-applied here.
type tokenizer struct {
	vocab map[string]int
	clsID int
	sepID int
	unkID int
	padID int
}

// newTokenizer reads a vocab.txt file (one token per line, line
// number = vocab ID) and constructs a ready-to-use tokenizer.
func newTokenizer(vocabPath string) (*tokenizer, error) {
	f, err := os.Open(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: open vocab %s: %w", vocabPath, err)
	}
	defer f.Close()
	t := &tokenizer{vocab: map[string]int{}}
	scanner := bufio.NewScanner(f)
	idx := 0
	for scanner.Scan() {
		word := scanner.Text()
		t.vocab[word] = idx
		switch word {
		case "[CLS]":
			t.clsID = idx
		case "[SEP]":
			t.sepID = idx
		case "[UNK]":
			t.unkID = idx
		case "[PAD]":
			t.padID = idx
		}
		idx++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: scan vocab: %w", err)
	}
	return t, nil
}

// encode tokenizes text into BERT token IDs framed with [CLS] /
// [SEP]. The returned slice is exactly the input the model expects
// for input_ids. attentionMask is all-ones the same length; the
// caller pads to a fixed seqLen via encodeBatch when batching.
func (t *tokenizer) encode(text string) (ids []int) {
	ids = append(ids, t.clsID)
	for _, word := range basicTokenize(strings.ToLower(text)) {
		for _, piece := range t.wordPieceTokenize(word) {
			id, ok := t.vocab[piece]
			if !ok {
				id = t.unkID
			}
			ids = append(ids, id)
		}
	}
	ids = append(ids, t.sepID)
	return ids
}

// encodeBatch encodes one or more sentences and returns three
// padded int64 slices of length len(sentences)*seqLen:
// input_ids (padded with PAD token), attention_mask (1 for real,
// 0 for pad), token_type_ids (always 0 — single-segment input).
//
// Sentences longer than seqLen are truncated and [SEP] is moved
// to the last position; shorter sentences are padded with padID.
// This matches the input contract for HuggingFace BERT models.
func (t *tokenizer) encodeBatch(sentences []string, seqLen int) (ids, mask, types []int64) {
	n := len(sentences)
	ids = make([]int64, n*seqLen)
	mask = make([]int64, n*seqLen)
	types = make([]int64, n*seqLen) // all zeros — single segment
	for si, s := range sentences {
		raw := t.encode(s)
		if len(raw) > seqLen {
			raw = raw[:seqLen]
			raw[seqLen-1] = t.sepID
		}
		base := si * seqLen
		for i, id := range raw {
			ids[base+i] = int64(id)
			mask[base+i] = 1
		}
		for i := len(raw); i < seqLen; i++ {
			ids[base+i] = int64(t.padID)
			// mask + types remain 0
		}
	}
	return ids, mask, types
}

// basicTokenize splits text on whitespace + punctuation. Each
// resulting word is then passed to wordPieceTokenize for vocab
// matching. Lowercase normalization is the caller's responsibility
// (encode lowercases before calling — MiniLM is uncased).
func basicTokenize(text string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsSpace(r) {
			flush()
		} else if unicode.IsPunct(r) {
			flush()
			words = append(words, string(r))
		} else {
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

// wordPieceTokenize matches word against vocab, splitting into the
// longest known prefix + "##"-prefixed remainders for the rest.
// Falls back to [UNK] when no prefix is in vocab.
func (t *tokenizer) wordPieceTokenize(word string) []string {
	runes := []rune(word)
	if len(runes) == 0 {
		return nil
	}
	var pieces []string
	start := 0
	for start < len(runes) {
		end := len(runes)
		found := false
		for end > start {
			substr := string(runes[start:end])
			if start > 0 {
				substr = "##" + substr
			}
			if _, ok := t.vocab[substr]; ok {
				pieces = append(pieces, substr)
				found = true
				break
			}
			end--
		}
		if !found {
			return []string{"[UNK]"}
		}
		start = end
	}
	return pieces
}
