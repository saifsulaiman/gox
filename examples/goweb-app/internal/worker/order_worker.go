package worker

import (
	"log/slog"

	"github.com/goxlang/gox/pkg/goweb"
)

// RegisterWorkers sets up background message consumers on the RabbitMQ client.
func RegisterWorkers(app *goweb.Engine) {
	app.Queue().ConsumeJSON("orders.fulfill", func(msg goweb.Message, payload map[string]any) error {
		slog.Info("Received order fulfillment event",
			slog.Any("order_id", payload["order_id"]),
			slog.String("routing_key", msg.RoutingKey),
		)
		return nil
	})
}
