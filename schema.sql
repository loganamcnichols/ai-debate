-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";


-- Create the survey table
CREATE TABLE survey (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  lucid_id INT UNIQUE,
  chat_time INT,
  create_time TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);


-- Create the response table
CREATE TABLE response (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  survey_id UUID REFERENCES survey(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  panelist_id UUID,
  supplier_id UUID,
  age INT,
  zip TEXT DEFAULT '',
  gender TEXT  DEFAULT '',
  hispanic TEXT DEFAULT '',
  ethnicity TEXT DEFAULT '',
  standard_vote TEXT DEFAULT '',
  which_llm TEXT DEFAULT '',
  ai_speed TEXT DEFAULT '',
  musk_opinion TEXT DEFAULT '',
  patterson_opinion TEXT DEFAULT '',
  kensington_opinion TEXT DEFAULT '',
  potholes TEXT DEFAULT '',
  start_time TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
  completed BOOLEAN DEFAULT TRUE,
  innovate_first BOOLEAN DEFAULT FALSE
);

-- Create the chat table
CREATE TABLE chat (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  response_id UUID REFERENCES response(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  user_msg TEXT NOT NULL,
  safety_msg TEXT DEFAULT '',
  innovation_msg TEXT DEFAULT '',
  created_time TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Create an index on the foreign key for better performance
CREATE INDEX idx_chat_response_id ON chat(response_id);
CREATE INDEX idx_response_survey_id ON response(survey_id);