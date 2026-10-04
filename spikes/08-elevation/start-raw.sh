#!/bin/sh
cd "$(dirname "$0")" && claude --model haiku "$(cat raw-prompt.txt)"
