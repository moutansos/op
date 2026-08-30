CC = gcc

all: link
	@echo "Compiling all files..."

compile:
	$(CC) -g -Wall -Wextra -D_GNU_SOURCE -c main.c fzflib.c configlib.c pathlib.c

link: compile
	$(CC) -o main main.o fzflib.o configlib.o pathlib.o
