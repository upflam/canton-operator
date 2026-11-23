#!/usr/bin/env python3
"""Async HTTP server wrapping laya.Agent.system_one().

Exposes:
  GET  /health          → {"status":"ok"}
  POST /v1/systemone    → laya inference (same contract as laya.go expects)

Runs on Starlette/uvicorn. The model's forward pass is synchronous and CPU-bound,
so it is offloaded to a threadpool — the event loop stays free and concurrent
requests don't serialize behind one inference.
"""
import os

import laya
import uvicorn
from starlette.applications import Starlette
from starlette.concurrency import run_in_threadpool
from starlette.responses import JSONResponse
from starlette.routing import Route

_agent = None


async def health(request):
    return JSONResponse({"status": "ok"})


async def systemone(request):
    body = await request.json()
    result = await run_in_threadpool(_agent.system_one, body["state"], body["questions"])
    return JSONResponse(result)


app = Starlette(
    routes=[
        Route("/health", health, methods=["GET"]),
        Route("/v1/systemone", systemone, methods=["POST"]),
    ]
)


if __name__ == "__main__":
    model_id = os.environ.get("LAYA_MODEL", "ameerhmz5/laya-modernbert-decision-90pct")
    port = int(os.environ.get("LAYA_PORT", 8000))
    host = os.environ.get("LAYA_HOST", "0.0.0.0")

    print(f"loading model {model_id} …", flush=True)
    _agent = laya.load(model_id)
    print(f"ready — serving on {host}:{port}", flush=True)

    uvicorn.run(app, host=host, port=port, log_level="warning")
