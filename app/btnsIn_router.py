
from database import dataUtil as db
from utils import keyboards as kb
from utils import BigMessages as bigMessages
from aiogram import Router, F
from aiogram.filters import CommandStart
from aiogram.types import CallbackQuery, Message
import asyncio

main_router = Router()


@main_router.message(CommandStart(deep_link=True))
async def main_deepLinker(message: Message, command: CommandStart):
    user_id = message.from_user.id
    arg = command.args

    if arg and arg.startswith("nft"):
        main_message = await message.answer("Получаю данные...")

        result = await db.search_nft(uid=arg.replace("nft", ""))
        user = await db.search_user(user_id=user_id)

        if result:
            data_aboutNft, link, uid, from_user = result
            name, link, UUID = await db.search_nftPlusAndDelete(uid, user_id)

            if name is not None:
                await asyncio.sleep(0.3)

                await main_message.edit_text(
                    f'✨ Получен NFT: <a href="{link}"><b>{data_aboutNft}</b></a>\n'
                    f'<blockquote><code>Модель: {name}\n'
                    f'Ссылка: {link}\n'
                    f'UID в блоке Cloud: {UUID}</code></blockquote>',
                    reply_markup=await kb.main_menuRefferer()
                )

                await main_message.bot.send_message(
                    chat_id=from_user,
                    text=f'Кнопка активирована. НФТ получен.\n'
                         f'Получатель: @{message.from_user.username or "@None"} '
                         f'(id: {message.from_user.id})'
                )
            else:
                await main_message.edit_text('❌ NFT не найден.')
        else:
            await main_message.edit_text("❌ NFT не найден.")

        return

    await message.answer(
        text=await bigMessages.main_menu(),
        reply_markup=await kb.main_menu()
    )


@main_router.message(CommandStart(deep_link=False))
async def start_handler(message: Message):
    await message.answer(
        text=await bigMessages.main_menu(),
        reply_markup=await kb.main_menu()
    )


@main_router.callback_query(F.data == 'main_menu')
async def cabinet_handler(call: CallbackQuery):
    await call.message.delete()
    await call.message.answer(
        text=await bigMessages.main_menu(),
        reply_markup=await kb.main_menu()
    )
    await call.answer()


@main_router.callback_query(F.data == 'cabinet')
async def main_cabinetWithNFTS(call: CallbackQuery):
    user_id = call.from_user.id
    await call.message.delete()

    user_text = await bigMessages.main_cabinetWithNft(user_id)

    if user_text is not None:
        await call.message.answer(
            text=user_text,
            reply_markup=await kb.main_menuCabinet()
        )
    else:
        await call.message.answer(
            'Вы не зарегистрированы. Попробуйте написать /start'
        )

    await call.answer()


@main_router.callback_query(F.data == 'blocked')
async def main_Blocked(call: CallbackQuery):
    await call.answer(
        '❌This function is currently unavalible in your country. '
        'Stay with us, we will resume soon our work.',
        show_alert=True
    )


@main_router.callback_query(F.data == 'go_ref')
async def main_Goreffer(call: CallbackQuery):
    user_text = await bigMessages.GoRef(user_id=call.from_user.id)

    await call.message.delete()
    await call.message.answer(
        text=user_text,
        reply_markup=await kb.main_BotRefferer()
    )
    await call.answer()


@main_router.callback_query(F.data == 'check')
async def main_deniedCheck(call: CallbackQuery):
    await call.message.edit_text('checking...')
    await asyncio.sleep(2)

    user_text = await bigMessages.GoRef(user_id=call.from_user.id)

    await call.message.delete()
    await call.message.answer(
        text=user_text,
        reply_markup=await kb.main_BotRefferer()
    )
    await call.answer()


@main_router.callback_query(F.data == 'faq_bot')
async def main_botFAQ(call: CallbackQuery):
    faq_message = await bigMessages.main_FAQ()

    await call.message.delete()
    await call.message.answer(
        text=faq_message,
        reply_markup=await kb.back_menu()
    )
    await call.answer()
