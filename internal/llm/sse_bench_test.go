package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/message"
)

type sseBenchDumpMeta struct {
	Provider  string   `json:"provider"`
	SSEChunks []string `json:"sse_chunks"`
}

type sseBenchFixture struct {
	Name       string
	Provider   string
	Path       string
	ChunkCount int
	BodyBytes  []byte
}

type sseBenchCorpus struct {
	Provider string
	Entries  []sseBenchFixture
}

type sseBenchCandidate struct {
	path       string
	provider   string
	chunkCount int
}

var (
	sseBenchCorpusOnce sync.Once
	sseBenchCorpusData map[string]sseBenchCorpus
	sseBenchCorpusErr  error
)

var openAICallbackFixedFixture = []byte(strings.Join([]string{
	`data: {"id":"chatcmpl-fixed","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Plan: "}}]}`,
	`data: {"id":"chatcmpl-fixed","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"content":"inspect files"}}]}`,
	`data: {"id":"chatcmpl-fixed","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"Read"}}]}}]}`,
	`data: {"id":"chatcmpl-fixed","model":"gpt-5.5-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"README.md\",\"limit\":120}"}}]}}]}`,
	`data: {"id":"chatcmpl-fixed","model":"gpt-5.5-mini","choices":[{"index":0,"finish_reason":"tool_calls"}]}`,
	`data: [DONE]`,
	"",
}, "\n"))

var responsesCallbackFixedFixture = []byte(strings.Join([]string{
	`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Bash"}}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"echo hi"}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\",\"timeout\":30}"}`,
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Bash","status":"completed"}}`,
	`data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`,
	`data: [DONE]`,
	"",
}, "\n"))

var responsesWSCallbackFixedFixture = []byte(strings.Join([]string{
	`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_ws_1","call_id":"call_ws","name":"Read"}}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\""}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"internal/llm/sse_bench_test.go"}`,
	`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\",\"limit\":64}"}`,
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_ws_1","call_id":"call_ws","name":"Read","status":"completed"}}`,
	`data: {"type":"response.completed","response":{"id":"resp-ws-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`,
	`data: [DONE]`,
	"",
}, "\n"))

type fixedSSEBenchFixture struct {
	provider string
	name     string
	body     []byte
}

func loadFixedCallbackFixtures() []fixedSSEBenchFixture {
	return []fixedSSEBenchFixture{
		{provider: "openai", name: "openai_fixed", body: openAICallbackFixedFixture},
		{provider: "responses", name: "responses_fixed", body: responsesCallbackFixedFixture},
		{provider: "responses_ws", name: "responses_ws_fixed", body: responsesWSCallbackFixedFixture},
	}
}

func BenchmarkSSEParseWithCallbackCumulative(b *testing.B) {
	benchmarkSSEParseWithCallbackMode(b, false)
}

func BenchmarkSSEParseWithCallbackIncremental(b *testing.B) {
	benchmarkSSEParseWithCallbackMode(b, true)
}

func benchmarkSSEParseWithCallbackMode(b *testing.B, incremental bool) {
	fixtures := loadFixedCallbackFixtures()
	providers := []string{"openai", "responses", "responses_ws"}
	for _, provider := range providers {
		var fixture fixedSSEBenchFixture
		ok := false
		for _, candidate := range fixtures {
			if candidate.provider == provider {
				fixture = candidate
				ok = true
				break
			}
		}
		if !ok || len(fixture.body) == 0 {
			continue
		}
		mode := "cumulative"
		if incremental {
			mode = "incremental"
		}
		b.Run(provider+"/fixed/"+mode, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.body)))
			for i := 0; i < b.N; i++ {
				var sink int
				lastByID := map[string]string{}
				cb := func(delta message.StreamDelta) {
					switch delta.Type {
					case "text", "thinking":
						sink += len(delta.Text)
					case "tool_use_delta":
						if delta.ToolCall != nil {
							input := delta.ToolCall.Input
							if incremental {
								prev := lastByID[delta.ToolCall.ID]
								input = strings.TrimPrefix(input, prev)
								lastByID[delta.ToolCall.ID] = delta.ToolCall.Input
							}
							sink += len(input)
						}
					}
				}
				reader := bytes.NewReader(fixture.body)
				var (
					resp *message.Response
					err  error
				)
				switch provider {
				case "openai":
					resp, err = parseOpenAISSEStream(reader, cb, nil)
				case "responses", "responses_ws":
					resp, err = parseResponsesSSE(reader, cb, nil)
				default:
					b.Fatalf("unsupported provider %q", provider)
				}
				if err != nil {
					b.Fatalf("parse fixed fixture %s: %v", fixture.name, err)
				}
				if resp == nil {
					b.Fatalf("parse fixed fixture %s returned nil response", fixture.name)
				}
				if sink < 0 {
					b.Fatal("unreachable")
				}
			}
		})
	}
}

func BenchmarkSSEParseWithCollector(b *testing.B) {
	b.StopTimer()
	corpora, err := loadSSEBenchCorpora()
	b.StartTimer()
	if err != nil {
		b.Fatalf("load SSE benchmark corpus: %v", err)
	}
	providers := []string{"openai", "responses", "responses_ws"}
	for _, provider := range providers {
		corpus, ok := corpora[provider]
		if !ok || len(corpus.Entries) == 0 {
			continue
		}
		fixture := corpus.Entries[0]
		for _, entry := range corpus.Entries {
			if entry.ChunkCount > fixture.ChunkCount {
				fixture = entry
			}
		}
		b.Run(provider+"/largest_with_collector", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.BodyBytes)))
			for i := 0; i < b.N; i++ {
				reader := bytes.NewReader(fixture.BodyBytes)
				collector := NewSSECollector()
				var (
					resp *message.Response
					err  error
				)
				switch provider {
				case "openai":
					resp, err = parseOpenAISSEStream(reader, nil, collector)
				case "responses", "responses_ws":
					resp, err = parseResponsesSSE(reader, nil, collector)
				default:
					b.Fatalf("unsupported provider %q", provider)
				}
				if err != nil {
					b.Fatalf("parse fixture %s: %v", fixture.Path, err)
				}
				if resp == nil {
					b.Fatalf("parse fixture %s returned nil response", fixture.Path)
				}
				if len(collector.Chunks()) == 0 {
					b.Fatalf("collector got no chunks for %s", fixture.Path)
				}
			}
		})
	}
}

func BenchmarkSSEParseReplay(b *testing.B) {
	b.StopTimer()
	corpora, err := loadSSEBenchCorpora()
	b.StartTimer()
	if err != nil {
		b.Fatalf("load SSE benchmark corpus: %v", err)
	}
	if len(corpora) == 0 {
		b.Skip("no SSE benchmark corpus found under the local dump corpus directory")
	}

	providers := make([]string, 0, len(corpora))
	for provider := range corpora {
		providers = append(providers, provider)
	}
	sort.Strings(providers)

	for _, provider := range providers {
		corpus := corpora[provider]
		if len(corpus.Entries) == 0 {
			continue
		}
		b.Run(provider+"/corpus/full", func(b *testing.B) {
			benchmarkSSECorpusFull(b, corpus)
		})
		b.Run(provider+"/corpus/scan_only", func(b *testing.B) {
			benchmarkSSECorpusScanOnly(b, corpus)
		})
		for _, fixture := range corpus.Entries {
			fixture := fixture
			b.Run(provider+"/"+fixture.Name+"/full", func(b *testing.B) {
				benchmarkSSEFixtureFull(b, fixture)
			})
			b.Run(provider+"/"+fixture.Name+"/scan_only", func(b *testing.B) {
				benchmarkSSEFixtureScanOnly(b, fixture)
			})
		}
	}
}

func benchmarkSSECorpusFull(b *testing.B, corpus sseBenchCorpus) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(avgFixtureBodyBytes(corpus.Entries)))
	for i := 0; i < b.N; i++ {
		fixture := corpus.Entries[i%len(corpus.Entries)]
		resp, err := parseSSEBenchFixture(fixture)
		if err != nil {
			b.Fatalf("parse fixture %s: %v", fixture.Path, err)
		}
		if resp == nil {
			b.Fatalf("parse fixture %s returned nil response", fixture.Path)
		}
	}
}

func benchmarkSSEFixtureFull(b *testing.B, fixture sseBenchFixture) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.BodyBytes)))
	for i := 0; i < b.N; i++ {
		resp, err := parseSSEBenchFixture(fixture)
		if err != nil {
			b.Fatalf("parse fixture %s: %v", fixture.Path, err)
		}
		if resp == nil {
			b.Fatalf("parse fixture %s returned nil response", fixture.Path)
		}
	}
}

func benchmarkSSEFixtureScanOnly(b *testing.B, fixture sseBenchFixture) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.BodyBytes)))
	for i := 0; i < b.N; i++ {
		if err := scanOnlySSEFixture(fixture); err != nil {
			b.Fatalf("scan fixture %s: %v", fixture.Path, err)
		}
	}
}

func benchmarkSSECorpusScanOnly(b *testing.B, corpus sseBenchCorpus) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(avgFixtureBodyBytes(corpus.Entries)))
	for i := 0; i < b.N; i++ {
		fixture := corpus.Entries[i%len(corpus.Entries)]
		if err := scanOnlySSEFixture(fixture); err != nil {
			b.Fatalf("scan fixture %s: %v", fixture.Path, err)
		}
	}
}

func parseSSEBenchFixture(fixture sseBenchFixture) (*message.Response, error) {
	reader := bytes.NewReader(fixture.BodyBytes)
	switch fixture.Provider {
	case "anthropic":
		return parseSSEStream(reader, nil, nil)
	case "openai":
		return parseOpenAISSEStream(reader, nil, nil)
	case "responses", "responses_ws":
		return parseResponsesSSE(reader, nil, nil)
	default:
		return nil, fmt.Errorf("unsupported provider %q", fixture.Provider)
	}
}

func scanOnlySSEFixture(fixture sseBenchFixture) error {
	scanner := bufio.NewScanner(bytes.NewReader(fixture.BodyBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch fixture.Provider {
		case "anthropic":
			if strings.HasPrefix(line, "event: ") || strings.HasPrefix(line, "data: ") || line == "" {
				continue
			}
		case "openai", "responses", "responses_ws":
			if strings.HasPrefix(line, "data:") || line == "" {
				continue
			}
		}
	}
	return scanner.Err()
}

func avgFixtureBodyBytes(fixtures []sseBenchFixture) int {
	if len(fixtures) == 0 {
		return 0
	}
	total := 0
	for _, fixture := range fixtures {
		total += len(fixture.BodyBytes)
	}
	return total / len(fixtures)
}

func loadSSEBenchCorpora() (map[string]sseBenchCorpus, error) {
	sseBenchCorpusOnce.Do(func() {
		sseBenchCorpusData, sseBenchCorpusErr = buildSSEBenchCorpora()
	})
	return sseBenchCorpusData, sseBenchCorpusErr
}

func buildSSEBenchCorpora() (map[string]sseBenchCorpus, error) {
	dumpsDir := filepath.Join(repoRootFromThisFile(), ".chord", "llm_dumps")
	entries, err := os.ReadDir(dumpsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dumps dir: %w", err)
	}

	candidatesByProvider := map[string][]sseBenchCandidate{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dumpsDir, entry.Name())
		meta, err := readSSEBenchDumpMeta(path)
		if err != nil {
			continue
		}
		if len(meta.SSEChunks) == 0 {
			continue
		}
		provider := meta.Provider
		switch provider {
		case "anthropic", "openai", "responses", "responses_ws":
		default:
			continue
		}
		candidatesByProvider[provider] = append(candidatesByProvider[provider], sseBenchCandidate{
			path:       path,
			provider:   provider,
			chunkCount: len(meta.SSEChunks),
		})
	}

	corpora := make(map[string]sseBenchCorpus)
	for provider, candidates := range candidatesByProvider {
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].chunkCount == candidates[j].chunkCount {
				return candidates[i].path < candidates[j].path
			}
			return candidates[i].chunkCount < candidates[j].chunkCount
		})
		selected := selectRepresentativeCandidates(candidates)
		fixtures := make([]sseBenchFixture, 0, len(selected))
		for _, cand := range selected {
			meta, err := readSSEBenchDumpMeta(cand.path)
			if err != nil {
				continue
			}
			body, err := reconstructSSEBenchmarkBody(cand.provider, meta.SSEChunks)
			if err != nil {
				continue
			}
			fixture := sseBenchFixture{
				Name:       fixtureNameFromCandidate(cand, candidates),
				Provider:   cand.provider,
				Path:       cand.path,
				ChunkCount: cand.chunkCount,
				BodyBytes:  body,
			}
			if _, err := parseSSEBenchFixture(fixture); err != nil {
				continue
			}
			fixtures = append(fixtures, fixture)
		}
		if len(fixtures) > 0 {
			corpora[provider] = sseBenchCorpus{Provider: provider, Entries: fixtures}
		}
	}
	return corpora, nil
}

func selectRepresentativeCandidates(candidates []sseBenchCandidate) []sseBenchCandidate {
	if len(candidates) <= 3 {
		return append([]sseBenchCandidate(nil), candidates...)
	}
	positions := []int{
		quantileIndex(len(candidates), 0.10),
		quantileIndex(len(candidates), 0.50),
		quantileIndex(len(candidates), 0.95),
	}
	seen := make(map[int]bool)
	selected := make([]sseBenchCandidate, 0, len(positions))
	for _, idx := range positions {
		if seen[idx] {
			continue
		}
		seen[idx] = true
		selected = append(selected, candidates[idx])
	}
	return selected
}

func fixtureNameFromCandidate(candidate sseBenchCandidate, all []sseBenchCandidate) string {
	if len(all) <= 3 {
		return fmt.Sprintf("chunks_%d", candidate.chunkCount)
	}
	small := all[quantileIndex(len(all), 0.10)]
	median := all[quantileIndex(len(all), 0.50)]
	large := all[quantileIndex(len(all), 0.95)]
	switch candidate.path {
	case small.path:
		return fmt.Sprintf("small_%d", candidate.chunkCount)
	case median.path:
		return fmt.Sprintf("median_%d", candidate.chunkCount)
	case large.path:
		return fmt.Sprintf("large_%d", candidate.chunkCount)
	default:
		return fmt.Sprintf("chunks_%d", candidate.chunkCount)
	}
}

func quantileIndex(n int, q float64) int {
	if n <= 1 {
		return 0
	}
	idx := int(float64(n-1) * q)
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

func readSSEBenchDumpMeta(path string) (*sseBenchDumpMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var meta sseBenchDumpMeta
	if err := json.NewDecoder(f).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func reconstructSSEBenchmarkBody(provider string, chunks []string) ([]byte, error) {
	var buf bytes.Buffer
	switch provider {
	case "anthropic":
		for _, chunk := range chunks {
			eventType, payload, ok := strings.Cut(chunk, ": ")
			if !ok {
				return nil, fmt.Errorf("anthropic dump chunk missing event prefix: %q", chunk)
			}
			buf.WriteString("event: ")
			buf.WriteString(eventType)
			buf.WriteByte('\n')
			buf.WriteString("data: ")
			buf.WriteString(payload)
			buf.WriteString("\n\n")
		}
	case "openai", "responses", "responses_ws":
		for _, chunk := range chunks {
			buf.WriteString("data: ")
			buf.WriteString(chunk)
			buf.WriteString("\n\n")
		}
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	return buf.Bytes(), nil
}

func repoRootFromThisFile() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
