"""OTel wiring for the agent — logs + metrics to the local LGTM stack.

Python-side mirror of tools/telemetry/bridge.py: everything ships to the same
collector under service.name "sebastian-agent", so Grafana shows the device
half and the agent half of every conversation side by side.

Fails soft: with the collector down (or deps missing) the SDK queues/drops in
background threads and the agent keeps working. Disable with SEBASTIAN_OTEL=0.
"""

import logging
import os

_METER = None


class _NullInstrument:
    def add(self, *args, **kwargs) -> None: ...


def counter(name: str, description: str = ""):
    if _METER is None:
        return _NullInstrument()
    return _METER.create_counter(name, description=description)


def setup() -> None:
    global _METER
    if os.getenv("SEBASTIAN_OTEL", "1") == "0":
        return
    try:
        from opentelemetry import metrics
        from opentelemetry._logs import set_logger_provider
        from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
        from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
        from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
        from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
        from opentelemetry.sdk.metrics import MeterProvider
        from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
        from opentelemetry.sdk.resources import Resource
    except ImportError as e:
        logging.getLogger(__name__).warning("otel deps missing, telemetry off: %s", e)
        return

    endpoint = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
    resource = Resource.create({"service.name": "sebastian-agent"})

    reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=f"{endpoint}/v1/metrics"), export_interval_millis=5000
    )
    metrics.set_meter_provider(MeterProvider(resource=resource, metric_readers=[reader]))
    _METER = metrics.get_meter("sebastian.agent")

    provider = LoggerProvider(resource=resource)
    provider.add_log_record_processor(
        BatchLogRecordProcessor(OTLPLogExporter(endpoint=f"{endpoint}/v1/logs"))
    )
    set_logger_provider(provider)
    handler = LoggingHandler(logger_provider=provider)
    # The SDK logs its own export failures through stdlib logging — shipping
    # those back through this handler would feed the failure it reports.
    handler.addFilter(lambda record: not record.name.startswith("opentelemetry"))
    logging.getLogger().addHandler(handler)
