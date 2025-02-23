package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fogleman/primitive/primitive"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nfnt/resize"
)

const (
	INPUT_FILENAME  = "triangles-in.png"
	OUTPUT_FILENAME = "triangles-out.png"
)

var s Settings

type Settings struct {
	SecretKey        string        `envconfig:"SECRET_KEY"`
	UnsplashClientID string        `envconfig:"UNSPLASH_CLIENT_ID"`
	RelayURLs        []string      `envconfig:"RELAY_URLS"`
	AdditionalTags   []string      `envconfig:"ADDITIONAL_TAGS"`
	PostingDuration  time.Duration `envconfig:"POSTING_DURATION"`
	PoW              int           `envconfig:"POW"`
}

func main() {
	log.Printf("starting %s", stringVersion())

	if err := envconfig.Process("", &s); err != nil {
		log.Fatalf("failed to read from env: %s", err)
		return
	}

	ticker := time.NewTicker(s.PostingDuration)
	defer ticker.Stop()

	// uploads on start.
	log.Printf("posting first event...")
	upload()

	log.Printf("posting every %s", s.PostingDuration)
	for range ticker.C {
		log.Print("posting new kind 20...")
		upload()
	}
}

func upload() {
	log.Print("getting image from unsplash...")
	// get random picture from unsplash
	resp, err := http.Get("https://api.unsplash.com/photos/random?client_id=" +
		s.UnsplashClientID + "&topics=nature,cathedral,outdoors,landscape,cafe,restaurante")
	if err != nil {
		log.Printf("unsplash call failed: %s", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		log.Printf("unsplash read failed: %s", data)
	}

	var unsp struct {
		ID   string `json:"id"`
		URLs struct {
			Regular string `json:"regular"`
		} `json:"urls"`
		Links struct {
			HTML string `json:"html"`
		} `json:"links"`
		BlurHash string `json:"blur_hash"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Desc     string `json:"alt_description"`
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
		Tags []struct {
			Title string `json:"title"`
		} `json:"tags"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&unsp); err != nil {
		log.Printf("unsplash decode failed: %s", err)
	}

	log.Print("creating temp image file...")
	// prepare files (this is not really necessary, we should just load stuff from memory)
	inputpath := filepath.Join(os.TempDir(), INPUT_FILENAME)
	outputpath := filepath.Join(os.TempDir(), OUTPUT_FILENAME)
	defer os.RemoveAll(inputpath)
	defer os.RemoveAll(outputpath)

	// download file
	resp, err = http.Get(unsp.URLs.Regular)
	if err != nil {
		log.Printf("failed to download picture: %s", err)
		return
	}
	defer resp.Body.Close()
	file, err := os.Create(inputpath)
	if err != nil {
		log.Printf("failed to create file: %s", err)
		return
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		log.Printf("failed to save picture: %s", err)
		return
	}

	log.Print("processing image...")
	// generate primitive image
	rand.New(rand.NewSource(time.Now().UTC().UnixNano()))

	input, err := primitive.LoadImage(inputpath)
	if err != nil {
		log.Printf("failed to create primitive: %s", err)
		return
	}

	if _, err := io.Copy(file, resp.Body); err != nil {
		log.Printf("failed to create primitive: %s", err)
		return
	}
	size := uint(256)
	if size > 0 {
		input = resize.Thumbnail(size, size, input, resize.Bilinear)
	}
	bg := primitive.MakeColor(primitive.AverageImageColor(input))
	model := primitive.NewModel(input, bg, 1024, 1)
	for i := 0; i < 225; i++ {
		model.Step(primitive.ShapeTypeTriangle, 128, 0)
	}
	if err := primitive.SavePNG(outputpath, model.Context.Image()); err != nil {
		log.Printf("failed to save primitive png: %s", err)
		return
	}

	log.Print("authorizing and uploading processed image to blossom server...")
	// publish to satellite
	uploadEvent := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      22242,
		Content:   "Authorize Upload",
		Tags: nostr.Tags{
			nostr.Tag{"name", "unsplash-" + unsp.ID},
		},
	}
	if err := uploadEvent.Sign(s.SecretKey); err != nil {
		log.Printf("failed to sign upload: %s", err)
		return
	}

	u, _ := url.Parse("https://api.satellite.earth/v1/media/item")
	qs := u.Query()
	qs.Add("auth", uploadEvent.String())
	u.RawQuery = qs.Encode()

	file, err = os.Open(outputpath)
	if err != nil {
		log.Printf("failed to open file: %s", err)
		return
	}
	defer file.Close()

	req, _ := http.NewRequest("PUT", u.String(), file)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("failed to upload: %s", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		log.Printf("failed to upload: %s", string(data))
		return
	}

	var image struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&image); err != nil {
		log.Printf("failed to decode response from satellite: %s", err)
		return
	}

	log.Print("creating kind 20 post...")
	content := fmt.Sprintf("%s\n", unsp.Desc)
	for _, t := range unsp.Tags {
		t.Title = strings.Replace(t.Title, " ", "_", 10)
		content += fmt.Sprintf(" #%s ", t.Title)
	}

	for _, t := range s.AdditionalTags {
		content += fmt.Sprintf(" #%s ", t)
	}

	tags := nostr.Tags{}
	tags = append(tags, nostr.Tag{
		"title",
		unsp.Desc,
	})

	tags = append(tags, nostr.Tag{
		"imeta",
		fmt.Sprintf("url %s", image.URL),
		"m image/jpeg",
		fmt.Sprintf("blurhash %s", unsp.BlurHash),
		fmt.Sprintf("dim %dx%d", unsp.Width, unsp.Height),
		fmt.Sprintf("alt %s", unsp.Desc),
		fmt.Sprintf("location %s", unsp.Location.Name),
		"fallback https://nostrcheck.me/alt2.jpg",
		"fallback https://void.cat/alt2.jpg",
	})

	tags = append(tags, nostr.Tag{
		"imeta",
		fmt.Sprintf("url %s", unsp.URLs.Regular),
		"m image/jpeg",
		fmt.Sprintf("blurhash %s", unsp.BlurHash),
		fmt.Sprintf("dim %dx%d", unsp.Width, unsp.Height),
		fmt.Sprintf("alt %s", unsp.Desc),
		fmt.Sprintf("location %s", unsp.Location.Name),
		"fallback https://nostrcheck.me/alt2.jpg",
		"fallback https://void.cat/alt2.jpg",
	})

	for _, t := range unsp.Tags {
		tags = append(tags, nostr.Tag{"t", t.Title})
	}

	for _, t := range s.AdditionalTags {
		tags = append(tags, nostr.Tag{"t", t})
	}

	pubkey, _ := nostr.GetPublicKey(s.SecretKey)

	// publish nostr event
	event := nostr.Event{
		PubKey:    pubkey,
		CreatedAt: nostr.Now(),
		Kind:      20,
		Content:   content,
		Tags:      tags,
	}

	log.Print("doing work...")
	pow, err := nip13.DoWork(context.Background(), event, s.PoW)
	if err != nil {
		log.Printf("can't do pow: %s", err)
		return
	}

	event.Tags = append(event.Tags, pow)

	event.Sign(s.SecretKey)

	log.Print("signed event: ", event.String())

	for _, ru := range s.RelayURLs {
		log.Printf("publising to relay: %s", ru)
		relay, err := nostr.RelayConnect(context.Background(), ru)
		if err != nil {
			log.Printf("failed to connect: %s", err)
			relay.Close()

			continue
		}

		if err := relay.Publish(context.Background(), event); err != nil {
			log.Printf("failed to publish: %s", err)
			relay.Close()

			continue
		}

		relay.Close()
	}

	nevent, _ := nip19.EncodeEvent(event.ID, s.RelayURLs, "")
	fmt.Println("https://njump.me/" + nevent)
}
