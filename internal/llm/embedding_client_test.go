package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenAIEmbeddingClientUsesCompatibleEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "embedding-model" || request.Input != "记忆检索" {
			t.Errorf("request = %#v", request)
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.25,0.75]}]}`))
	}))
	defer server.Close()
	client := &OpenAIEmbeddingClient{APIKey: "test-key", BaseURL: server.URL, Model: "embedding-model"}
	got, err := client.Embed(context.Background(), "记忆检索")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []float32{0.25, 0.75}) {
		t.Fatalf("vector = %v", got)
	}
}

func TestOpenAIEmbeddingClientEmbedsBatchInInputOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "embedding-model" || !reflect.DeepEqual(request.Input, []string{"first", "second"}) {
			t.Errorf("request = %#v", request)
		}
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[2,2]},{"index":0,"embedding":[1,1]}]}`))
	}))
	defer server.Close()

	client := &OpenAIEmbeddingClient{APIKey: "test-key", BaseURL: server.URL, Model: "embedding-model"}
	got, err := client.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1, 1}, {2, 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vectors = %v, want %v", got, want)
	}
}

func TestOpenAIEmbeddingClientEmptyBatchDoesNotRequireConfiguration(t *testing.T) {
	got, err := (*OpenAIEmbeddingClient)(nil).EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("vectors = %v, want empty", got)
	}
}
