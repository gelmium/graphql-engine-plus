import redis
import asyncio
import logging
import os

logger = logging.getLogger("scripting-exec-cache")


class InternalExecCache(dict):

    content_length = {}

    def set_redis_instance(self, redis: redis.Redis):
        self.redis = redis

    def __delitem__(self, key) -> None:
        # TODO: should we delete from redis as well
        self.content_length.pop(key)
        return super().__delitem__(key)

    async def get(self, key):
        if hasattr(self, "redis"):
            exec_content = await self.redis.get(key)
            if exec_content is None:
                # if key is already deleted in redis, just return None
                # we dont want to use internal cache in this scenario
                return None
            if self.content_length.get(key) == len(exec_content):
                # if the content length is the same, we can just return the function in memory
                return super().get(key)
            logger.info(f"Load {key} from redis")
            exec(exec_content)
            exec_func = locals()["main"]
            # update the new function to memory using the content from redis
            self.__setitem__(key, exec_func)
            # update the new content length as well
            self.content_length[key] = len(exec_content)
            return exec_func
        # if redis is not available, just return the function in memory
        return super().get(key)

    async def execute_get_main(self, key, exec_content):
        if self.content_length.get(key) == len(exec_content):
            exec_func = self.__getitem__(key)
            if exec_func:
                return exec_func
        try:
            exec(exec_content)
            exec_main_func = locals()["main"]
        except KeyError as e:
            if "main" in str(e):
                raise ValueError(
                    "The script must define this function: `async def main(request, body):`"
                )
            raise e
        else:
            await self.setdefault(key, exec_main_func, exec_content)
        return exec_main_func

    async def setdefault(self, key, exec_func, exec_content):
        if super().__contains__(key) is False and hasattr(self, "redis"):
            # save the function content to redis if available
            # setnx is SET if key does not exist
            task = asyncio.get_running_loop().create_task(
                self.redis.setnx(key, exec_content)
            )
            task.add_done_callback(
                lambda _: logger.info(f"Save {key} to redis if not exist")
            )
        # also save the function in memory
        super().setdefault(key, exec_func)
        # save the content length
        self.content_length.setdefault(key, len(exec_content))

    async def set(self, key, exec_func, exec_content):
        if hasattr(self, "redis"):
            # save the function content to redis if available
            # setnx is SET if key does not exist
            task = asyncio.get_running_loop().create_task(
                self.redis.set(key, exec_content)
            )
            task.add_done_callback(lambda _: logger.info(f"Save {key} to redis"))
        # also save the function in memory
        super().__setitem__(key, exec_func)
        # save the content length
        self.content_length[key] = len(exec_content)

    async def load_lib(self, lib_file_path, key_prefix="lib:"):
        """Load lib from redis"""
        if not hasattr(self, "redis"):
            raise ValueError("Redis instance is required for load_lib feature")
        key = f"{key_prefix}{lib_file_path}"
        lib_content = await self.redis.get(key)
        if lib_content is None:
            raise ValueError(f"Key {key} not found in redis")
        logger.info(f"Load {key} from redis")
        # write lib content to file
        BASE_SCRIPTS_PATH = "/graphql-engine/scripts"
        save_to = os.path.join(BASE_SCRIPTS_PATH, lib_file_path)
        with open(save_to, "w") as f:
            f.write(lib_content.decode("utf-8"))
