import redis
import asyncio
import logging

logger = logging.getLogger("scripting-exec-cache")


class InternalExecCache(dict):

    content_length = {}

    def set_redis_instance(self, redis: redis.Redis):
        self.redis = redis

    async def get(self, key):
        if hasattr(self, "redis"):
            exec_content = await self.redis.get(key)
            if exec_content is None:
                return None
            if self.content_length.get(key, 0) == len(exec_content):
                # if the content length is the same, we can just return the function in memory
                return super().get(key)
            logger.info(f"Load {key} from redis")
            exec(exec_content)
            exec_func = locals()["main"]
            # update the function in memory
            self.__setitem__(key, exec_func)
            # update the content length
            self.content_length[key] = len(exec_content)
            return exec_func
        # if redis is not available, just return the function in memory
        return super().get(key)

    async def setdefault(self, key, exec_func, exec_content):
        if hasattr(self, "redis"):
            # save the function content to redis if available
            task = asyncio.get_running_loop().create_task(
                self.redis.set(key, exec_content)
            )
            task.add_done_callback(lambda _: logger.info(f"Save {key} to redis"))
        # also save the function in memory
        super().setdefault(key, exec_func)
        # save the content length
        self.content_length[key] = len(exec_content)
