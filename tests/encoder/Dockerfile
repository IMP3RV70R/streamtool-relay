FROM streamtool-control-publisher:latest
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-gi gir1.2-gstreamer-1.0 gir1.2-gst-plugins-base-1.0 && rm -rf /var/lib/apt/lists/*
COPY pipeline.py /experiment/pipeline.py
COPY probe.py /experiment/probe.py
USER 65532:65532
ENTRYPOINT ["python3", "-u", "/experiment/pipeline.py"]
