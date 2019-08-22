package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func init() {

}

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath("/etc/gitlab-token-forward-authd")
	viper.AddConfigPath(".")

	viper.SetDefault("Server.Address", ":8080")
	viper.SetDefault("Server.AuthenticationRealm", "Please log in with your GitLab username and a personal access token.")
	viper.SetDefault("GitLab.URL", "http://localhost")
	viper.SetDefault("GitLab.AuthorizedUsers", []string{})
	viper.SetDefault("GitLab.AuthorizedGroups", []string{})
	viper.SetDefault("GitLab.CIUsername", "ci")

	viper.SetEnvPrefix("AUTHD")
	viper.AutomaticEnv()

	viper.ReadInConfig()

	httpClient := http.DefaultClient

	if len(os.Getenv("DEBUG")) <= 0 {
		gin.SetMode(gin.ReleaseMode)
	}

	// Set up server
	r := gin.Default()
	r.Any("/auth", func(ctx *gin.Context) {
		basicAuthUser, basicAuthPass, ok := ctx.Request.BasicAuth()
		if !ok {
			ctx.Header("WWW-Authenticate", viper.GetString("Server.AuthenticationRealm"))
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		baseURL, err := url.Parse(viper.GetString("gitlab.url"))
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, fmt.Errorf("can not parse GitLab URL from server configuration: %s", err.Error()))
			return
		}

		authorizedUsers := viper.GetStringSlice("GitLab.AuthorizedUsers")
		authorizedGroups := viper.GetStringSlice("GitLab.AuthorizedGroups")

		var groupsResponse *http.Response

		isInAuthorizedUsers := false
		for _, authorizedUser := range authorizedUsers {
			if basicAuthUser == authorizedUser {
				isInAuthorizedUsers = true
				break
			}
		}

		switch basicAuthUser {
		case viper.GetString("GitLab.CIUsername"):
			// Assume basicAuthPass to be job token
			jobToken := basicAuthPass
			u := baseURL.ResolveReference(&url.URL{
				Path: "./api/v4/groups",
			})
			groupsRequest, err := http.NewRequest("GET", u.String(), nil)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, err)
				return
			}
			groupsRequest.Header.Set("Job-Token", jobToken)
			groupsResponse, err = httpClient.Do(groupsRequest)

		default:
			personalAccessToken := basicAuthPass

			u := baseURL.ResolveReference(&url.URL{
				Path: "./api/v4/user",
			})
			authRequest, err := http.NewRequest("GET", u.String(), nil)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, err)
				return
			}
			authRequest.Header.Set("PRIVATE-TOKEN", personalAccessToken)
			log.Println("/api/v4/user")
			authResponse, err := httpClient.Do(authRequest)
			log.Println(authResponse.Status)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, fmt.Errorf("failed to authenticate against GitLab server: %s", err.Error()))
				return
			}
			if authResponse.StatusCode != http.StatusOK {
				ctx.Header("WWW-Authenticate", viper.GetString("Server.AuthenticationRealm"))
				ctx.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			defer authResponse.Body.Close()
			authResponseStruct := struct {
				Username string
			}{}
			if err = json.NewDecoder(authResponse.Body).Decode(&authResponseStruct); err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, fmt.Errorf("received invalid response from GitLab: %s", err.Error()))
				return
			}

			// Check whether this token is valid for the given username
			if authResponseStruct.Username != basicAuthUser {
				ctx.Header("WWW-Authenticate", viper.GetString("Server.AuthenticationRealm"))
				ctx.AbortWithStatus(http.StatusUnauthorized)
				return
			}

			u = baseURL.ResolveReference(&url.URL{
				Path: "./api/v4/groups",
			})
			groupsRequest, err := http.NewRequest("GET", u.String(), nil)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, err)
				return
			}
			groupsRequest.Header.Set("PRIVATE-TOKEN", personalAccessToken)
			log.Println("/api/v4/groups")
			groupsResponse, err = httpClient.Do(groupsRequest)
		}

		// Check assigned groups
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, fmt.Errorf("failed to authenticate against GitLab server: %s", err.Error()))
			return
		}
		if groupsResponse.StatusCode != http.StatusOK {
			ctx.Header("WWW-Authenticate", viper.GetString("Server.AuthenticationRealm"))
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		groups := []struct {
			Path string
		}{}
		if err = json.NewDecoder(groupsResponse.Body).Decode(&groups); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, fmt.Errorf("failed to authenticate against GitLab server: %s", err.Error()))
		}
		log.Println(groups)
		isInAuthorizedGroups := false

	groupLoop:
		for _, group := range groups {
			log.Println(">", group)
			for _, authorizedGroupName := range authorizedGroups {
				log.Println(">>", group, "=", authorizedGroupName)
				if group.Path == authorizedGroupName {
					isInAuthorizedGroups = true
					break groupLoop
				}
			}
		}

		isAuthorized := isInAuthorizedGroups || isInAuthorizedUsers
		if isAuthorized {
			ctx.Status(http.StatusOK)
		} else {
			ctx.Header("WWW-Authenticate", viper.GetString("Server.AuthenticationRealm"))
			ctx.Status(http.StatusUnauthorized)
		}
	})
	server := new(http.Server)
	server.Addr = viper.GetString("Server.Address")
	server.Handler = r
	go server.ListenAndServe()

	viper.WatchConfig()
	viper.OnConfigChange(func(_ fsnotify.Event) {
		if server.Addr != viper.GetString("Server.Address") {
			server.Shutdown(context.Background())

			server = new(http.Server)
			server.Addr = viper.GetString("Server.Address")
			server.Handler = r
			go server.ListenAndServe()
		}
	})

	// Listen for signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan,
		os.Interrupt,
		syscall.SIGTERM)
	log.Println("Signal received:", <-signalChan)

	// Shut down everything
	log.Println("Shutting down…")
	server.Shutdown(context.Background())

}
