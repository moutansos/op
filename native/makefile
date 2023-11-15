CC = gcc

all: link
	@echo "Compiling all files..."

compile:
	$(CC) -c main.c fzflib.c configlib.c

link: compile
	$(CC) -o main main.o fzflib.o configlib.o
