import asyncio
import traceback
import aiohttp
from bs4 import BeautifulSoup
from urllib.parse import urlparse
import random

AI_DEBATE = "https://ai-debate.org"
QUESTIONS = [
    "If AI keeps improving at its current speed what will happen?",
    "Do you think the current level of AI safety is enough?",
    "What has been the impact of laws about AI?",
    "What would happen if we slowed down AI?",
]

async def create_user(session):
    async with session.get(AI_DEBATE + "/") as response:
        html = await response.text()
        print(html)
        soup = BeautifulSoup(html, 'html.parser')
        form = soup.find('form')
        hx_tag = form.attrs["hx-post"]
        url_str = AI_DEBATE + hx_tag
        url = urlparse(url_str)
        response_id = url.query.split('=')[1]
        return response_id

async def submit_question(session, response_id, user_msg):
    async with session.post(
        AI_DEBATE + "/submit-question",
        params={"response-id": response_id},
        data={"user-msg": user_msg}
    ) as response:
        if response.status != 200:
            print(f"Received not 200 status code: {response.status}, {response.reason}")
        return await response.text()

async def stream_response(session, response_id, client_id):
    try:
        async with session.get(
            AI_DEBATE + "/chat",
            params={"response-id": response_id}
        ) as response:
            async for line in response.content:
                chunk = line.decode().strip()
                if chunk:
                    print(f"Client {client_id} received: {chunk}")
    except asyncio.CancelledError as ce:
        print(f"Client {client_id} stream cancelled. Reason: {ce}")
    except aiohttp.ClientError as ce:
        print(f"Client {client_id} encountered a client error: {ce}")
    except Exception as e:
        print(f"Client {client_id} encountered an unexpected error:")
        print(traceback.format_exc())
    finally:
        print(f"Client {client_id} stream ended")

async def simulate_client(session, client_id):
    response_id = await create_user(session)
    print(f"Client {client_id} created with response_id: {response_id}")
    
    # Start the streaming task
    stream_task = asyncio.create_task(stream_response(session, response_id, client_id))
    
    try:
        for _ in range(3):  # Simulate 3 question-answer interactions
            question = random.choice(QUESTIONS)
            print(f"Client {client_id} asking: {question}")
            await submit_question(session, response_id, question)
            print(f"Client {client_id} submitted question")
            await asyncio.sleep(random.uniform(1, 3))  # Random delay between questions
    finally:
        # Cancel the streaming task when we're done with questions
        stream_task.cancel()
        try:
            await stream_task
        except asyncio.CancelledError:
            pass

async def main():
    async with aiohttp.ClientSession() as session:
        tasks = [simulate_client(session, i) for i in range(50)]  # Simulate 5 clients
        await asyncio.gather(*tasks)

if __name__ == "__main__":
    asyncio.run(main())