-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Create the response table
CREATE TABLE response (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  start_time TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
  completed BOOLEAN DEFAULT FALSE,
  first_move_innovation BOOLEAN DEFAULT FALSE
);

-- Create the chat table
CREATE TABLE chat (
  id UUID PRIMARY KEY,
  response_id UUID REFERENCES response(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  user_msg TEXT NOT NULL,
  caution_msg TEXT DEFAULT '',
  innovation_msg TEXT DEFAULT '',
  created_time TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Create an index on the foreign key for better performance
CREATE INDEX idx_question_response_id ON chat(response_id);