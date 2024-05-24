#!/bin/bash
echo "Upload all scripts in example to remote server"

FOLDER_PATH="example/graphql-engine/scripts"

# recursively upload all files in the folder to the remote server using the following command
# curl -X POST $SCRIPTING_UPLOAD_PATH -F "file=@$file_path" -F "path=$folder_path" -H "X-Engine-Plus-Execute-Secret: $ENGINE_PLUS_EXECUTE_SECRET"

for file_path in $FOLDER_PATH/*; do
  # check if the file is a directory
  if [ -d "$file_path" ]; then
    echo "Skipping directory $file_path"
    continue
  fi
  
  # check if if file_path is a file contain the word "lib_" or "_lib"
  if [[ $file_path == *"lib_"* ]] || [[ $file_path == *"_lib"* ]]; then
    echo "Uploading lib $file_path"
    curl -X POST $SCRIPTING_UPLOAD_PATH -F "file=@$file_path" -F "is_library=true" -H "X-Engine-Plus-Execute-Secret: $ENGINE_PLUS_EXECUTE_SECRET"
    echo " Done!"
    continue
  fi
  echo "Uploading script $file_path"
  curl -X POST $SCRIPTING_UPLOAD_PATH -F "file=@$file_path" -H "X-Engine-Plus-Execute-Secret: $ENGINE_PLUS_EXECUTE_SECRET"
  echo " Done!"
done

for file_path in $FOLDER_PATH/validate/*; do
  echo "Uploading $file_path"
  curl -X POST $SCRIPTING_UPLOAD_PATH -F "file=@$file_path" -F "path=validate" -H "X-Engine-Plus-Execute-Secret: $ENGINE_PLUS_EXECUTE_SECRET"
  echo " Done!"
done