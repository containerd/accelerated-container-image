# Collect Overlaybd metrics with Prometheus

Prometheus is an open-source systems monitoring and alerting toolkit. You can configure overlaybd as a Prometheus target. This topic shows you how to configure overlaybd to use Prometheus.

## Configure Overlaybd

To configure overlaybd as a Prometheus target, you need to specify `uriPrefix` and `port` of the exporter, they can be set in `/etc/overlaybd-snapshotter/config.json` (overlaybd-snapshotter config path), this is an example.

```json
{
    "root": "/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd",
    "asyncRemove": false,
    "address": "/run/overlaybd-snapshotter/overlaybd.sock",
    "verbose": "info",
    "rwMode": "overlayfs",
    "logReportCaller": false,
    "autoRemoveDev": false,
    "exporterConfig": {
        "enable": true,
        "uriPrefix": "/metrics",
        "port": 9863
    }
}
```

After configuration, you can run overlaybd-snapshotter and curl `http://localhost:9863/metrics` to get Prometheus metrics.

## Configure Prometheus

To use Prometheus to monitor overlaybd, you should modify the `scrape_configs`, and add a `job_name` for overlaybd in `.../prometheus/prometheus.yml`.

```toml
# my global config
global:
  scrape_interval: 15s # Set the scrape interval to every 15 seconds. The default is every 1 minute.
  evaluation_interval: 15s # Evaluate rules every 15 seconds. The default is every 1 minute.
  # scrape_timeout is set to the global default (10s).

# Alertmanager configuration
alerting:
  alertmanagers:
    - static_configs:
        - targets:
          # - alertmanager:9093

# Load rules once and periodically evaluate them according to the global 'evaluation_interval'.
rule_files:
  # - "first_rules.yml"
  # - "second_rules.yml"

# A scrape configuration containing exactly one endpoint to scrape:
# Here it's Prometheus itself.
scrape_configs:
  # The job name is added as a label `job=<job_name>` to any timeseries scraped from this config.
  - job_name: "prometheus"

    # metrics_path defaults to '/metrics'
    # scheme defaults to 'http'.

    static_configs:
      - targets: ["localhost:9090"]
  - job_name: "overlaybd"
    metrics_path: "/metrics"
    static_configs:
      - targets: ["localhost:9863"]
```

In this configuration, you set a job named `overlaybd`, and set `metrics_path` equal to `uriPrefix` in the overlaybd configuration. Next, you can start a Prometheus service using this configuration to monitor overlaybd.