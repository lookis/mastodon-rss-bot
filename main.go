package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	b64 "encoding/base64"

	"github.com/mattn/go-mastodon"
	"github.com/mmcdole/gofeed"
	"github.com/spf13/viper"
)

type SourceDest struct {
	RSS         string `yaml:rss`
	AccessToken string `yaml:access_token`
}

type Config struct {
	Interval       time.Duration `yaml:interval`
	MastodonServer string        `yaml:server`
	AppID          string        `yaml:app_id`
	AppSecret      string        `yaml:app_secret`
	SourceDests    []SourceDest  `yaml:sources`
}

func updateProfile(ctx context.Context, masto *mastodon.Client, displayName string, imageUrl string) {
	currentUser, _ := masto.GetAccountCurrentUser(ctx)

	profile := &mastodon.Profile{}
	if currentUser.DisplayName != displayName {
		profile.DisplayName = &displayName
	}

	if resp, err := http.Get(imageUrl); err == nil {
		defer resp.Body.Close()
		if body, err := ioutil.ReadAll(resp.Body); err == nil {
			avatar := b64.StdEncoding.EncodeToString(body)
			profile.Avatar = avatar
		}
	}

	masto.AccountUpdate(ctx, profile)
}

func run(ctx context.Context, c *Config) error {
	for {
		masto := mastodon.NewClient(&mastodon.Config{
			Server:       c.MastodonServer,
			ClientID:     c.AppID,
			ClientSecret: c.AppSecret,
		})
		fp := gofeed.NewParser()
		select {
		case <-ctx.Done():
			return nil
		case <-time.Tick(c.Interval):

			for _, source := range c.SourceDests {
				if feed, err := fp.ParseURL(source.RSS); err == nil {
					// profileImage := feed.Image.URL
					// updateProfile(ctx, masto, feed.Title, profileImage)

				}
			}
		}
	}
}

func main() {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	viper.SetConfigName("config")                  // name of config file (without extension)
	viper.SetConfigType("yaml")                    // REQUIRED if the config file does not have the extension in the name
	viper.AddConfigPath("$HOME/.mastodon-rss-bot") // call multiple times to add many search paths
	viper.AddConfigPath(".")                       // optionally look for config in the working directory
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			fmt.Fprintf(os.Stdout, "No config file found. exit")
			os.Exit(0)
		} else {
			fmt.Fprintf(os.Stderr, "config file read error, exit")
			os.Exit(1)
		}
	}
	fmt.Println(viper.AllKeys())
	conf := &Config{}
	if err := viper.Unmarshal(conf); err != nil {
		fmt.Printf("unable to decode into config struct, %v", err)
		os.Exit(1)
	}

	defer func() {
		cancel()
	}()

	if err := run(ctx, conf); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}
