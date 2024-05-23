package main

import (
	"context"
	"log"
	"time"

	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel/sdk/resource"

	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type TraceOptions struct {
	tracer oteltrace.Tracer
	ctx    context.Context
}

func InitTracerProvider(ctx context.Context, otelTracerType string) *sdktrace.TracerProvider {
	var exporter *otlptrace.Exporter
	var err error
	if otelTracerType == "http" {
		client := otlptracehttp.NewClient()
		exporter, err = otlptrace.New(ctx, client)
	} else if otelTracerType == "grpc" {
		exporter, err = otlptracegrpc.New(ctx)
	} else {
		if otelTracerType != "" && otelTracerType != "false" {
			log.Println("Error, unknown Open Telemetry exporter: ", otelTracerType)
		}
		tp := sdktrace.NewTracerProvider()
		// calling Shutdown create a noops tracer provider
		_ = tp.Shutdown(ctx)
		return tp
	}
	if err != nil {
		log.Println("Error when init Open Telemetry tracer: ", err)
		tp := sdktrace.NewTracerProvider()
		// calling Shutdown create a noops tracer provider
		_ = tp.Shutdown(ctx)
		return tp
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(
			resource.NewWithAttributes(
				semconv.SchemaURL,
			)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(autoprop.NewTextMapPropagator())
	return tp
}

func WrapContextCancelByAnotherContext(mainCtx context.Context, triggerCtx context.Context, timeoutDuration time.Duration) (newCtx context.Context, cancel context.CancelFunc) {
	// create a new context with cancel
	if timeoutDuration > 0 {
		newCtx, cancel = context.WithTimeout(mainCtx, timeoutDuration*time.Millisecond)
	} else {
		newCtx, cancel = context.WithCancel(mainCtx)
	}
	// create a new goroutine to cancel the context
	go func() {
		select {
		case <-triggerCtx.Done():
			cancel()
		case <-newCtx.Done():
			// pass
		}
	}()
	// return ctx, cancel
	return
}

// FastHttpHeaderCarrier adapts fasthttp.RequestHeader to satisfy the TextMapCarrier interface.
type FastHttpHeaderCarrier struct {
	requestHeader *fasthttp.RequestHeader
}

// Get returns the value associated with the passed key.
func (hc FastHttpHeaderCarrier) Get(key string) string {
	return string(hc.requestHeader.Peek(key))
}

// Set stores the key-value pair.
func (hc FastHttpHeaderCarrier) Set(key string, value string) {
	hc.requestHeader.Set(key, value)
}

// Keys lists the keys stored in this carrier.
func (hc FastHttpHeaderCarrier) Keys() []string {
	var keys []string
	for _, key := range hc.requestHeader.PeekKeys() {
		keys = append(keys, string(key))
	}
	return keys
}
