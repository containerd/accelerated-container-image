# OpenTelemetry Tracing Support

The accelerated-container-image project includes OpenTelemetry (OTEL) tracing support to help monitor and debug image conversion operations. This document describes how to use and configure tracing in the project.

## Overview

Tracing is implemented using OpenTelemetry, which provides detailed insights into the image conversion process. Key operations that are traced include:

- Overall image conversion process
- Layer conversion operations
- Individual layer application and processing
- Remote layer operations (when using remote storage)

## Configuration

Tracing can be configured using standard OpenTelemetry environment variables:

- `OTEL_SERVICE_NAME`: Sets the service name for traces (default: "accelerated-container-image")
- `OTEL_EXPORTER_OTLP_ENDPOINT`: The endpoint where traces should be sent (e.g., "http://localhost:4317")
- `OTEL_EXPORTER_OTLP_PROTOCOL`: The protocol to use (default: "grpc")
- `ENVIRONMENT`: The environment name to be included in traces (e.g., "production", "staging")

## Key Spans and Attributes

The following spans are created during image conversion:

### Convert Operation
- Name: `Convert`
- Attributes:
  - `fsType`: The filesystem type being used
  - `layerCount`: Number of layers in the source image

### Layer Conversion
- Name: `convertLayers`
- Attributes:
  - `fsType`: The filesystem type being used
  - `layerCount`: Number of layers to convert

### Layer Application
- Name: `applyOCIV1LayerInObd`
- Attributes:
  - `layerDigest`: The digest of the layer being applied
  - `layerSize`: Size of the layer in bytes
  - `parentID`: ID of the parent snapshot

## Example Usage

1. Start your OpenTelemetry collector:
   ```bash
   docker run -d --name otel-collector \
     -p 4317:4317 \
     -p 4318:4318 \
     otel/opentelemetry-collector-contrib
   ```

2. Set the required environment variables:
   ```bash
   export OTEL_SERVICE_NAME="accelerated-container-image"
   export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4317"
   export ENVIRONMENT="development"
   ```

3. Run the image conversion as normal. Traces will be automatically collected and sent to your configured endpoint.

## Viewing Traces

Traces can be viewed in any OpenTelemetry-compatible tracing backend, such as:
- Jaeger
- Zipkin
- Grafana Tempo
- Cloud provider tracing services (e.g., AWS X-Ray, Google Cloud Trace)

## Troubleshooting

If you're not seeing traces:

1. Verify your OpenTelemetry collector is running and accessible
2. Check that the `OTEL_EXPORTER_OTLP_ENDPOINT` is correctly configured
3. Enable debug logging with the `--verbose` flag to see more detailed output
4. Check your collector's logs for any connection or configuration issues