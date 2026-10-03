package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tringify/cli/internal/upload"
)

// sampleUploadBatch is how many demo images go in one upload request.
const sampleUploadBatch = 10

// addSampleContent uploads the theme's demo images to the store's files and
// asks the store to add the demo catalog as products. The store does this in
// the background, on development stores only, skipping products it has.
func (a *app) addSampleContent(ctx context.Context, t *storeTarget, root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "demo", "catalog.json"))
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this theme has no demo/catalog.json, so there are no sample products to add")
	}
	if err != nil {
		return err
	}
	var pack map[string]any
	if err := json.Unmarshal(raw, &pack); err != nil {
		return fmt.Errorf("demo/catalog.json: %w", err)
	}
	names, err := demoImageNames(root)
	if err != nil {
		return err
	}
	a.printf("Uploading %d demo images to %s…\n", len(names), t.session.Account.Target.Name)
	fileIDs := map[string]string{}
	for start := 0; start < len(names); start += sampleUploadBatch {
		end := min(start+sampleUploadBatch, len(names))
		ids, err := a.uploadStoreImages(ctx, t, root, names[start:end])
		if err != nil {
			return err
		}
		for name, id := range ids {
			fileIDs[name] = id
		}
	}
	if _, err := t.client.Call(ctx, "add_sample_content", map[string]any{"pack": pack, "images": fileIDs}); err != nil {
		return err
	}
	a.println("Adding the sample products and collections. They appear under Products in a minute.")
	return nil
}

// demoImageNames lists demo/images, the folder the demo catalog refers to.
func demoImageNames(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "demo", "images"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (a *app) uploadStoreImages(ctx context.Context, t *storeTarget, root string, names []string) (map[string]string, error) {
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		info, err := os.Stat(filepath.Join(root, "demo", "images", name))
		if err != nil {
			return nil, err
		}
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		items = append(items, map[string]any{"original_filename": name, "mime_type": contentType, "size_bytes": info.Size()})
	}
	data, err := t.client.Call(ctx, "create_file_uploads", map[string]any{"file_type": "upload", "items": items})
	if err != nil {
		return nil, err
	}
	var created struct {
		Intents []struct {
			upload.Grant
			Filename string `json:"filename"`
		} `json:"intents"`
	}
	if err := decode(data, &created); err != nil {
		return nil, err
	}
	if len(created.Intents) != len(names) {
		return nil, errors.New("the store returned an unexpected number of uploads")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	ids := make([]string, 0, len(names))
	byIntent := map[string]string{}
	for i, intent := range created.Intents {
		if err := upload.Put(ctx, client, intent.Grant, filepath.Join(root, "demo", "images", names[i])); err != nil {
			return nil, fmt.Errorf("upload %s: %w", names[i], err)
		}
		ids = append(ids, intent.IntentID)
		byIntent[intent.IntentID] = names[i]
	}
	if _, err := t.client.Call(ctx, "finalize_file_uploads", map[string]any{"file_type": "upload", "intent_ids": ids}); err != nil {
		return nil, err
	}
	out := map[string]string{}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		data, err := t.client.Call(ctx, "get_file_upload_status", map[string]any{"file_type": "upload", "intent_ids": ids})
		if err != nil {
			return nil, err
		}
		var status struct {
			Intents []struct {
				IntentID string `json:"intent_id"`
				Status   string `json:"status"`
				File     *struct {
					ID string `json:"id"`
				} `json:"file"`
			} `json:"intents"`
		}
		if err := decode(data, &status); err != nil {
			return nil, err
		}
		pending := false
		for _, s := range status.Intents {
			switch s.Status {
			case "ready":
				if s.File != nil && s.File.ID != "" {
					out[byIntent[s.IntentID]] = s.File.ID
				}
			case "rejected", "expired":
				a.printf("Skipped %s: the store did not accept it.\n", byIntent[s.IntentID])
			default:
				pending = true
			}
		}
		if !pending {
			return out, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the demo images are still processing; run the command again in a minute")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
