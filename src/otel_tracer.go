package main

import (
	"context"
	"log"

	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/sdk/resource"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type TraceOptions struct {
	tracer oteltrace.Tracer
	ctx    context.Context
}

func InitTracerProvider(ctx context.Context, otelExporter string) *sdktrace.TracerProvider {
	var exporter *otlptrace.Exporter
	var err error
	if otelExporter == "http" {
		client := otlptracehttp.NewClient()
		exporter, err = otlptrace.New(ctx, client)
	} else if otelExporter == "grpc" {
		exporter, err = otlptracegrpc.New(ctx)
	} else {
		log.Println("Unknown Open Telemetry exporter: ", otelExporter)
		return oteltrace.NewNoopTracerProvider().(*sdktrace.TracerProvider)
	}
	if err != nil {
		log.Println("Error when init Open Telemetry tracer: ", err)
		return oteltrace.NewNoopTracerProvider().(*sdktrace.TracerProvider)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(
			resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceNameKey.String("graphql-engine-plus"), // TODO: from env
			)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp
}

func StartRootSpanForFiberHandler(c *fiber.Ctx, tracer oteltrace.Tracer) (context.Context, oteltrace.Span) {
	spanCtx, spanRoot := tracer.Start(c.Context(), c.Path(),
		oteltrace.WithNewRoot(),
	)
	return spanCtx, spanRoot
}
