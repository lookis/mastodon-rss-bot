package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"encoding/base64"

	"github.com/PuerkitoBio/goquery"
	"github.com/mattn/go-mastodon"
	"github.com/mmcdole/gofeed"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type SourceDest struct {
	RSS         string `mapstructure:"rss"`
	Username    string `mapstructure:"username"`
	Password    string `mapstructure:"password"`
	NameCleaner string `mapstructure:"name_cleaner,omitempty"`
	Cleaner     string `mapstructure:"cleaner,omitempty"`
	CleanText   string `mapstructure:"clean,omitempty"`
}

type Config struct {
	Debug     bool         `mapstructure:"debug"`
	Server    string       `mapstructure:"server"`
	AppId     string       `mapstructure:"app_id"`
	AppSecret string       `mapstructure:"app_secret"`
	Sources   []SourceDest `mapstructure:"sources"`
}

func updateProfile(ctx context.Context, masto *mastodon.Client, feed *gofeed.Feed, config SourceDest) {
	cleaner := regexp.MustCompile(config.NameCleaner)
	displayName := cleaner.ReplaceAllString(feed.Title, "")
	currentProfile, err := masto.GetAccountCurrentUser(ctx)
	if err != nil {
		logrus.Warn(err)
	}
	profile := &mastodon.Profile{}

	if currentProfile.DisplayName != displayName {
		profile.DisplayName = &displayName
		if feed.Image != nil {
			if resp, err := http.Get(feed.Image.URL); err == nil {
				defer resp.Body.Close()
				if body, err := ioutil.ReadAll(resp.Body); err == nil {
					avatar := base64.StdEncoding.EncodeToString(body)
					profile.Avatar = avatar
				} else {
					logrus.Warn(err)
				}
			} else {
				logrus.Warn(err)
			}
		}
	}
	logrus.Info(fmt.Sprintf("update profile with %+v\n", profile))
	masto.AccountUpdate(ctx, profile)
}

func syncStatus(ctx context.Context, masto *mastodon.Client, item *gofeed.Item, cleaner *regexp.Regexp, cleanText string) {
	cleanDescription := item.Description
	if cleaner != nil {
		cleanDescription = cleaner.ReplaceAllString(item.Description, cleanText)
	}
	if document, err := goquery.NewDocumentFromReader(strings.NewReader(cleanDescription)); err == nil {
		status := strings.TrimSpace(document.Text())
		urls := document.Find("img").Map(func(index int, selection *goquery.Selection) string {
			return selection.AttrOr("src", "")
		})
		if len(urls) > 4 {
			urls = urls[:4]
		}
		//upload all pics
		medias := []mastodon.ID{}
		for _, url := range urls {
			if resp, err := http.Get(url); err == nil {
				defer resp.Body.Close()
				if attachment, err := masto.UploadMediaFromReader(ctx, resp.Body); err == nil {
					medias = append(medias, attachment.ID)
				} else {
					logrus.Warn(err)
				}
			} else {
				logrus.Warn(err)
			}
		}
		if len(medias) > 0 {
			toot := &mastodon.Toot{
				Status:   status,
				MediaIDs: medias,
			}
			logrus.Debug(fmt.Sprintf("sync status with %d medias\n", len(medias)))
			_, err := masto.PostStatus(ctx, toot)
			if err != nil {
				logrus.Warn(err)
			}
		}
	} else {
		logrus.Warn(err)
	}
}

func run(ctx context.Context, c *Config) {
	fp := gofeed.NewParser()
	logrus.Info("syncing...")
	for _, source := range c.Sources { // for every diff rss source
		//init description cleaner to filter
		var cleaner *regexp.Regexp
		if len(source.Cleaner) != 0 {
			cleaner = regexp.MustCompile(source.Cleaner)
		}
		//create client
		masto := mastodon.NewClient(&mastodon.Config{
			Server:       c.Server,
			ClientID:     c.AppId,
			ClientSecret: c.AppSecret,
		})
		masto.Authenticate(ctx, source.Username, source.Password)
		//get rss feeds
		if feed, err := fp.ParseURL(source.RSS); err == nil {
			logrus.Info(fmt.Sprintf("read %s success\n", source.RSS))
			// try update profile
			updateProfile(ctx, masto, feed, source)
			if account, err := masto.GetAccountCurrentUser(ctx); err == nil {
				//get last posted status create time, only publish new status
				var latestStatusCreatedAt *time.Time = nil
				if statuses, err := masto.GetAccountStatuses(ctx, account.ID, nil); err == nil {
					if len(statuses) > 0 {
						latestStatusCreatedAt = &statuses[0].CreatedAt
					}
				} else {
					logrus.Warn(err)
				}
				// sort rss feed asc by create time
				sort.SliceStable(feed.Items, func(i, j int) bool {
					return feed.Items[i].PublishedParsed.Before(*feed.Items[j].PublishedParsed)
				})
				publishStatus := []*gofeed.Item{}
				//find new status
				for _, item := range feed.Items {
					if latestStatusCreatedAt == nil || item.PublishedParsed.After(*latestStatusCreatedAt) {
						publishStatus = append(publishStatus, item)
					} else {
						logrus.Debug(fmt.Sprintf("skip synced status: latest/%s feed/%s\n", latestStatusCreatedAt.String(), item.PublishedParsed.String()))
					}
				}
				//publish
				for _, item := range publishStatus {
					syncStatus(ctx, masto, item, cleaner, source.CleanText)
				}
				logrus.Info(fmt.Sprintf("published {%d} status", len(publishStatus)))
			} else {
				logrus.Warn(err)
			}
		} else {
			logrus.Warn(err)
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
			logrus.Info("No config file found. exit")
			os.Exit(0)
		} else {
			logrus.Warn("config file read error, exit")
			os.Exit(1)
		}
	}
	conf := &Config{Debug: false}
	if err := viper.Unmarshal(conf); err != nil {
		logrus.Warn(err)
		os.Exit(1)
	}
	if conf.Debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	defer func() {
		cancel()
	}()
	run(ctx, conf)
}
