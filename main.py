from aiogram import F, Bot, Dispatcher
from aiogram.types import Message
from utils.settings import TOKEN
from app.inline_router import inline_router
from app.btnsIn_router import main_router
from aiogram.client.default import DefaultBotProperties as de_fault
import asyncio
import threading

from web import run_web

bot = Bot(
    token=TOKEN,
    default=de_fault(
        parse_mode='HTML',
        link_preview_prefer_large_media=True
    )
)

dp = Dispatcher()

@dp.message(F.photo)
async def get_photo(message: Message):
    photo = message.photo[-1]
    file_id = photo.file_id
    await message.answer(f"file_id:\n<code>{file_id}</code>")

async def main():
    dp.include_routers(main_router, inline_router)
    await dp.start_polling(bot)

if __name__ == '__main__':
    threading.Thread(target=run_web, daemon=True).start()
    asyncio.run(main())