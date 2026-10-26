// Command server runs the customer service as a long-running HTTP server.
package main

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/tahananazkaban/jemlih/pkg/server"
	"github.com/tahananazkaban/jemlih/services/customer/internal/app"
)

func main() {
	server.Main("customer", func(awsCfg aws.Config) (http.Handler, error) {
		cfg, err := app.ConfigFromEnv(awsCfg)
		if err != nil {
			return nil, err
		}
		return app.New(cfg)
	})
}
