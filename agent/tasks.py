"""Shared background-task registry.

asyncio only keeps a weak reference to a task, so an un-retained create_task()
can be garbage-collected mid-flight. spawn() holds a strong reference until the
task completes, then drops it.
"""

import asyncio
import logging
from typing import Any, Coroutine

log = logging.getLogger("sebastian.agent.tasks")

_bg_tasks: set[asyncio.Task[Any]] = set()


def _report(task: asyncio.Task[Any]) -> None:
    _bg_tasks.discard(task)
    if task.cancelled():
        return
    if (exc := task.exception()) is not None:
        log.error("background task %s died: %r", task.get_name(), exc, exc_info=exc)


def spawn(coro: Coroutine[Any, Any, Any]) -> asyncio.Task[Any]:
    task = asyncio.create_task(coro)
    _bg_tasks.add(task)
    task.add_done_callback(_report)
    return task
