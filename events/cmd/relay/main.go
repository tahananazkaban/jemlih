// Command relay is the Lambda that forwards outbox items from every service table's stream to the bus.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/tahananazkaban/jemlih/events/relay"
	"github.com/tahananazkaban/jemlih/pkg/awsx"
	"github.com/tahananazkaban/jemlih/pkg/env"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "events-relay"))
	awsCfg, err := awsx.Load(context.Background())
	if err != nil {
		slog.Error("load aws config", "err", err)
		os.Exit(1)
	}
	r := &relay.Relay{Bus: eventbridge.NewFromConfig(awsCfg), BusName: env.String("EVENT_BUS_NAME", "jemlih")}
	lambda.Start(r.Handle)
}
