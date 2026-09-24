package hf

import "testing"

func TestResolveURL_DatasetUsesDatasetsPrefix(t *testing.T) {
	c := &hubClient{baseURL: "https://huggingface.co", repoClass: RepoClassDataset}
	got := c.resolveURL("hf-internal-testing/dataset_with_data_files", "c19550d", "data/train.txt")
	want := "https://huggingface.co/datasets/hf-internal-testing/dataset_with_data_files/resolve/c19550d/data/train.txt"
	if got != want {
		t.Fatalf("dataset resolve URL: got %q want %q", got, want)
	}
}

func TestResolveURL_ModelOmitsDatasetsPrefix(t *testing.T) {
	c := &hubClient{baseURL: "https://huggingface.co", repoClass: RepoClassModel}
	got := c.resolveURL("hf-internal-testing/tiny-random-gpt2", "71034c5", "config.json")
	want := "https://huggingface.co/hf-internal-testing/tiny-random-gpt2/resolve/71034c5/config.json"
	if got != want {
		t.Fatalf("model resolve URL: got %q want %q", got, want)
	}
}
