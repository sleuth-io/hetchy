# syntax=docker/dockerfile:1

# ─────────────────────────────────────────
# Stage 1 – dependency installation
# ─────────────────────────────────────────
FROM node:22-alpine AS deps

WORKDIR /app

# Install dependencies required by some native modules
RUN apk add --no-cache libc6-compat

# Copy only the manifests first so Docker can cache this layer
COPY package.json package-lock.json* yarn.lock* pnpm-lock.yaml* ./

# Install production + dev dependencies (needed for the build step)
RUN \
  if [ -f pnpm-lock.yaml ]; then \
    corepack enable && corepack prepare pnpm@latest --activate && pnpm install --frozen-lockfile; \
  elif [ -f yarn.lock ]; then \
    yarn install --frozen-lockfile; \
  else \
    npm ci; \
  fi

# ─────────────────────────────────────────
# Stage 2 – build
# ─────────────────────────────────────────
FROM node:22-alpine AS builder

WORKDIR /app

# Re-use the installed node_modules from the deps stage
COPY --from=deps /app/node_modules ./node_modules

# Copy the rest of the source
COPY . .

# Build the application (adjust the script name if yours differs)
RUN \
  if [ -f pnpm-lock.yaml ]; then \
    corepack enable && corepack prepare pnpm@latest --activate && pnpm run build; \
  elif [ -f yarn.lock ]; then \
    yarn build; \
  else \
    npm run build; \
  fi

# ─────────────────────────────────────────
# Stage 3 – production runtime
# ─────────────────────────────────────────
FROM node:22-alpine AS runner

WORKDIR /app

# Security: run as a non-root user
RUN addgroup --system --gid 1001 appgroup && \
    adduser  --system --uid 1001 --ingroup appgroup appuser

# Set production environment
ENV NODE_ENV=production
ENV PORT=3000

# Copy only what is needed to run the app
COPY --from=builder --chown=appuser:appgroup /app/package.json  ./package.json
COPY --from=builder --chown=appuser:appgroup /app/node_modules ./node_modules
COPY --from=builder --chown=appuser:appgroup /app/dist         ./dist
# Uncomment the line below if your framework outputs to a 'build' directory instead
# COPY --from=builder --chown=appuser:appgroup /app/build ./build

USER appuser

EXPOSE 3000

# Health-check so orchestrators know when the container is ready
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:3000/health || exit 1

# Use the JSON (exec) form so signals are forwarded correctly
CMD ["node", "dist/index.js"]
