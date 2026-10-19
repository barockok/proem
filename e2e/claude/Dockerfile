# Real Claude Code, pinned. Bump CLAUDE_CODE_VERSION deliberately: a protocol
# change in a new release then shows up as a failing e2e run.
FROM node:22-slim
ARG CLAUDE_CODE_VERSION=2.1.197
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && claude --version
# Production pilots also set forceLoginMethod: "gateway". Headless mode
# (CLAUDE_CODE_USE_GATEWAY with ANTHROPIC_AUTH_TOKEN) refuses to start with it,
# so the smoke sets only the gateway URL.
COPY e2e/claude/managed-settings.json /etc/claude-code/managed-settings.json
COPY e2e/claude/smoke.mjs /smoke.mjs
ENTRYPOINT ["node", "/smoke.mjs"]
