"""Shared background-task registry.

asyncio only keeps a weak reference to a task, so an un-retained create_task()
can be garbage-collected mid-flight. spawn() holds a strong reference until the
task completes, then drops it.
"""

import asyncio
from typing import Any, Coroutine

_bg_tasks: set[asyncio.Task[Any]] = set()


def spawn(coro: Coroutine[Any, Any, Any]) -> asyncio.Task[Any]:
    task = asyncio.create_task(coro)
    _bg_tasks.add(task)
    task.add_done_callback(_bg_tasks.discard)
    return task
