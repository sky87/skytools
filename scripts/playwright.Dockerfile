FROM skytools-runtime
USER root
RUN skytools deps install --only playwright-cli && rm -rf /var/lib/apt/lists/*
COPY scripts/check-playwright.sh /checks/check-playwright.sh
USER dev
